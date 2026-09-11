// Command strata-verify performs offline verification of a comparison
// credential. Given only the issuer's public key, the credential JSON, the
// exact exported CSV bytes and the signed revocation list, it prints a single
// definite verdict and exits accordingly:
//
//	0 valid        signature good, bytes match, within validity, not revoked
//	2 expired      signature and bytes good, but the credential past expiry
//	3 revoked      credential id is present in the signed revocation list
//	4 crl_stale    revocation list past NextUpdate, revocation unknown
//	1 invalid      signature mismatch, altered bytes, malformed inputs
//
// No network access or service connection is required.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/attest"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/signing"
	"os"
	"time"
)

const (
	exitOK       = 0
	exitInvalid  = 1
	exitExpired  = 2
	exitRevoked  = 3
	exitStaleCRL = 4
)

type report struct {
	Verdict       string     `json:"verdict"`
	Reason        string     `json:"reason"`
	CredentialID  string     `json:"credential_id,omitempty"`
	ComparisonID  string     `json:"comparison_id,omitempty"`
	KeyID         string     `json:"issuer_key_id,omitempty"`
	ContentDigest string     `json:"content_digest,omitempty"`
	ContentLength int        `json:"content_length,omitempty"`
	ExpiresAt     *time.Time `json:"expires_at,omitempty"`
	RevokedAt     *time.Time `json:"revoked_at,omitempty"`
	RevokeReason  string     `json:"revoke_reason,omitempty"`
}

func main() {
	code, reportValue := run(os.Args[1:])
	emit(reportValue)
	os.Exit(code)
}

func run(args []string) (int, report) {
	fail := func(err error) (int, report) {
		return exitInvalid, report{Verdict: attest.VerdictInvalid, Reason: err.Error()}
	}
	flags := flag.NewFlagSet("strata-verify", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	publicPath := flags.String("public", "", "签发方公钥 PEM 文件（必填）")
	credentialPath := flags.String("credential", "", "凭据 JSON 文件（必填）")
	contentPath := flags.String("content", "", "待核验的导出 CSV 文件（必填，按原始字节读取）")
	crlPath := flags.String("crl", "", "签发方吊销列表 JSON 文件（必填）")
	nowValue := flags.String("now", "", "可选：覆盖核验当前时间（RFC3339），用于复核过期结论")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK, report{}
		}
		return fail(err)
	}
	if len(flags.Args()) > 0 {
		return fail(fmt.Errorf("不支持位置参数"))
	}
	if *publicPath == "" || *credentialPath == "" || *contentPath == "" || *crlPath == "" {
		return fail(fmt.Errorf("必须提供 -public -credential -content -crl 四个输入"))
	}
	publicPEM, err := os.ReadFile(*publicPath)
	if err != nil {
		return fail(fmt.Errorf("读取公钥失败: %w", err))
	}
	public, keyID, err := signing.ParsePublicKeyPEM(publicPEM)
	if err != nil {
		return fail(err)
	}
	cred, err := readJSON[attest.Credential](*credentialPath)
	if err != nil {
		return fail(fmt.Errorf("读取凭据失败: %w", err))
	}
	list, err := readJSON[attest.CRL](*crlPath)
	if err != nil {
		return fail(fmt.Errorf("读取吊销列表失败: %w", err))
	}
	content, err := os.ReadFile(*contentPath)
	if err != nil {
		return fail(fmt.Errorf("读取导出内容失败: %w", err))
	}
	now := time.Now().UTC()
	if *nowValue != "" {
		now, err = time.Parse(time.RFC3339, *nowValue)
		if err != nil {
			return fail(fmt.Errorf("-now 必须为 RFC3339 时间: %w", err))
		}
	}
	decision := attest.Verify(cred, list, content, public, keyID, now)
	out := report{
		Verdict:       decision.Verdict,
		Reason:        decision.Reason,
		CredentialID:  decision.Credential.Payload.CredentialID,
		ComparisonID:  decision.Credential.Payload.ComparisonID,
		KeyID:         decision.Credential.Payload.IssuerKeyID,
		ContentDigest: decision.Credential.Payload.ContentDigest,
		ContentLength: decision.Credential.Payload.ContentLength,
		RevokedAt:     decision.RevokedAt,
		RevokeReason:  decision.RevokeReason,
	}
	if !decision.Credential.Payload.ExpiresAt.IsZero() {
		at := decision.Credential.Payload.ExpiresAt
		out.ExpiresAt = &at
	}
	code := exitInvalid
	switch decision.Verdict {
	case attest.VerdictValid:
		code = exitOK
	case attest.VerdictExpired:
		code = exitExpired
	case attest.VerdictRevoked:
		code = exitRevoked
	case attest.VerdictStaleCRL:
		code = exitStaleCRL
	}
	return code, out
}

func readJSON[T any](path string) (T, error) {
	var value T
	raw, err := os.ReadFile(path)
	if err != nil {
		return value, err
	}
	if err = json.Unmarshal(raw, &value); err != nil {
		return value, err
	}
	return value, nil
}

func emit(value report) {
	raw, _ := json.MarshalIndent(value, "", "  ")
	fmt.Println(string(raw))
}
