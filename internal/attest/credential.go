// Package attest defines verifiable credentials for exported comparison
// results. A credential signs the exact bytes of the external CSV export, and
// a separately signed revocation list lets a verifier decide offline whether
// the credential is still trustworthy.
package attest

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Verdict values returned to recipients.
const (
	VerdictValid    = "valid"
	VerdictExpired  = "expired"
	VerdictRevoked  = "revoked"
	VerdictInvalid  = "invalid"
	VerdictStaleCRL = "crl_stale"
)

// CRLNextUpdateWindow is how long a signed revocation list stays
// authoritative. After this the verifier must refuse to trust it rather than
// silently accepting potentially revoked credentials.
const CRLNextUpdateWindow = 24 * time.Hour

// leeway absorbs clock differences between issuer and verifier.
const leeway = 60 * time.Second

// CredentialPayload is the signed part of a comparison credential. It binds
// the comparison to the exact exported CSV bytes, not to a result identifier
// or a precomputed summary field.
type CredentialPayload struct {
	Version       int       `json:"version"`
	Kind          string    `json:"kind"`
	CredentialID  string    `json:"credential_id"`
	ComparisonID  string    `json:"comparison_id"`
	ExportVersion string    `json:"export_version"`
	ContentType   string    `json:"content_type"`
	ContentDigest string    `json:"content_digest"`
	ContentLength int       `json:"content_length"`
	IssuedAt      time.Time `json:"issued_at"`
	NotBefore     time.Time `json:"not_before"`
	ExpiresAt     time.Time `json:"expires_at"`
	IssuerKeyID   string    `json:"issuer_key_id"`
}

// Credential is the public envelope handed to recipients. Only Payload is
// signed; the signature covers its canonical encoding.
type Credential struct {
	Payload   CredentialPayload `json:"payload"`
	Signature string            `json:"signature"`
}

// Revocation marks one credential as no longer trustworthy.
type Revocation struct {
	CredentialID string    `json:"credential_id"`
	ComparisonID string    `json:"comparison_id"`
	RevokedAt    time.Time `json:"revoked_at"`
	Reason       string    `json:"reason,omitempty"`
}

// CRLPayload is the signed revocation list.
type CRLPayload struct {
	Version     int          `json:"version"`
	Kind        string       `json:"kind"`
	IssuerKeyID string       `json:"issuer_key_id"`
	ThisUpdate  time.Time    `json:"this_update"`
	NextUpdate  time.Time    `json:"next_update"`
	Revocations []Revocation `json:"revocations"`
}

// CRL is the signed envelope for the revocation list.
type CRL struct {
	Payload   CRLPayload `json:"payload"`
	Signature string     `json:"signature"`
}

func canonical(value any) ([]byte, error) {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	// json.Encoder appends a trailing newline; strip it so the signed input
	// is exactly the compact canonical JSON object.
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// Signer signs credentials and revocation lists.
type Signer interface {
	KeyID() string
	Sign(message []byte) []byte
}

// Issuer is the production signer backed by an Ed25519 private key.
type Issuer struct {
	keyID string
	sign  func([]byte) []byte
}

func NewIssuer(s Signer) *Issuer {
	return &Issuer{keyID: s.KeyID(), sign: s.Sign}
}

func (i *Issuer) KeyID() string { return i.keyID }

func decodeHex(field, value string, want int) ([]byte, error) {
	raw, err := hex.DecodeString(value)
	if err != nil || len(raw) != want {
		return nil, fmt.Errorf("%s 必须为 %d 字节的十六进制", field, want)
	}
	return raw, nil
}

// NewCredential builds and signs a credential for the given export bytes.
func (i *Issuer) NewCredential(comparisonID, exportVersion, contentType string, content []byte, issuedAt time.Time, ttl time.Duration) (Credential, error) {
	if !validID(comparisonID, "cmp_") {
		return Credential{}, fmt.Errorf("对比结果编号无效")
	}
	if len(content) == 0 {
		return Credential{}, fmt.Errorf("导出内容为空，不能签发")
	}
	if ttl < time.Hour || ttl > 90*24*time.Hour {
		return Credential{}, fmt.Errorf("有效期必须在 1 小时到 90 天之间")
	}
	issuedAt = issuedAt.UTC()
	sum := sha256.Sum256(content)
	id := credentialID(comparisonID, sum[:], issuedAt)
	payload := CredentialPayload{
		Version:       1,
		Kind:          "strata-comparison-credential",
		CredentialID:  id,
		ComparisonID:  comparisonID,
		ExportVersion: exportVersion,
		ContentType:   contentType,
		ContentDigest: hex.EncodeToString(sum[:]),
		ContentLength: len(content),
		IssuedAt:      issuedAt,
		NotBefore:     issuedAt,
		ExpiresAt:     issuedAt.Add(ttl),
		IssuerKeyID:   i.keyID,
	}
	raw, err := canonical(payload)
	if err != nil {
		return Credential{}, err
	}
	return Credential{Payload: payload, Signature: hex.EncodeToString(i.sign(raw))}, nil
}

// SignCRL signs a revocation list version.
func (i *Issuer) SignCRL(version int, revoked []Revocation, thisUpdate time.Time) (CRL, error) {
	if version < 1 {
		return CRL{}, fmt.Errorf("吊销列表版本必须为正整数")
	}
	thisUpdate = thisUpdate.UTC()
	entries := make([]Revocation, len(revoked))
	copy(entries, revoked)
	payload := CRLPayload{
		Version:     version,
		Kind:        "strata-revocation-list",
		IssuerKeyID: i.keyID,
		ThisUpdate:  thisUpdate,
		NextUpdate:  thisUpdate.Add(CRLNextUpdateWindow),
		Revocations: entries,
	}
	raw, err := canonical(payload)
	if err != nil {
		return CRL{}, err
	}
	return CRL{Payload: payload, Signature: hex.EncodeToString(i.sign(raw))}, nil
}

func validID(id, prefix string) bool {
	if !strings.HasPrefix(id, prefix) || len(id) != len(prefix)+32 {
		return false
	}
	for _, c := range id[len(prefix):] {
		if !(c >= '0' && c <= '9') && !(c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// credentialID binds the credential to comparison, exported content and issue
// time, so re-issuing after expiry or content change yields a new identifier.
func credentialID(comparisonID string, digest []byte, issuedAt time.Time) string {
	input := append([]byte(comparisonID+"|"), digest...)
	input = append(input, '|')
	input = issuedAt.UTC().AppendFormat(input, time.RFC3339Nano)
	sum := sha256.Sum256(input)
	return "att_" + hex.EncodeToString(sum[:16])
}

// CredentialID exposes the deterministic identifier for external callers.
func CredentialID(comparisonID string, content []byte, issuedAt time.Time) string {
	sum := sha256.Sum256(content)
	return credentialID(comparisonID, sum[:], issuedAt)
}

// verifyCanonical checks the signature of a payload under the given key and
// requires the envelope's issuer key id to match.
func verifyCanonical(public *ed25519.PublicKey, keyID string, payload any, signatureHex, expectedKeyID string) error {
	if expectedKeyID != keyID {
		return fmt.Errorf("凭据签发密钥 %s 与所用公钥 %s 不匹配", expectedKeyID, keyID)
	}
	signature, err := decodeHex("signature", signatureHex, ed25519.SignatureSize)
	if err != nil {
		return err
	}
	raw, err := canonical(payload)
	if err != nil {
		return err
	}
	if !ed25519.Verify(*public, raw, signature) {
		return fmt.Errorf("签名验证失败")
	}
	return nil
}

// VerifyCredentialSignature checks only the credential signature.
func VerifyCredentialSignature(cred Credential, public ed25519.PublicKey, keyID string) error {
	if cred.Payload.Version != 1 || cred.Payload.Kind != "strata-comparison-credential" {
		return fmt.Errorf("凭据版本或类型不受支持")
	}
	if cred.Payload.IssuerKeyID == "" {
		return fmt.Errorf("凭据缺少签发密钥标识")
	}
	return verifyCanonical(&public, keyID, cred.Payload, cred.Signature, cred.Payload.IssuerKeyID)
}

// VerifyCRLSignature checks only the revocation list signature.
func VerifyCRLSignature(list CRL, public ed25519.PublicKey, keyID string) error {
	if list.Payload.Version < 1 || list.Payload.Kind != "strata-revocation-list" {
		return fmt.Errorf("吊销列表版本或类型不受支持")
	}
	if list.Payload.IssuerKeyID == "" {
		return fmt.Errorf("吊销列表缺少签发密钥标识")
	}
	if err := verifyCanonicalList(public, keyID, list); err != nil {
		return err
	}
	if !list.Payload.NextUpdate.Equal(list.Payload.ThisUpdate.Add(CRLNextUpdateWindow)) {
		return fmt.Errorf("吊销列表有效期窗口被改动")
	}
	return nil
}

func verifyCanonicalList(public ed25519.PublicKey, keyID string, list CRL) error {
	return verifyCanonical(&public, keyID, list.Payload, list.Signature, list.Payload.IssuerKeyID)
}

// Decision is the complete offline verification conclusion.
type Decision struct {
	Verdict      string
	Reason       string
	Credential   Credential
	RevokedAt    *time.Time
	RevokeReason string
}

// Verify performs the full offline check of a credential against the exact
// exported bytes and a signed revocation list. Every failure yields a definite
// verdict; it never returns a vague error once the inputs parse.
func Verify(cred Credential, list CRL, content []byte, public ed25519.PublicKey, keyID string, now time.Time) Decision {
	if err := VerifyCredentialSignature(cred, public, keyID); err != nil {
		return Decision{Verdict: VerdictInvalid, Reason: err.Error(), Credential: cred}
	}
	p := cred.Payload
	if !validID(p.ComparisonID, "cmp_") || !validID(p.CredentialID, "att_") {
		return Decision{Verdict: VerdictInvalid, Reason: "凭据中的编号格式无效", Credential: cred}
	}
	if !p.NotBefore.Before(p.ExpiresAt) {
		return Decision{Verdict: VerdictInvalid, Reason: "凭据有效期为空或倒置", Credential: cred}
	}
	digest, err := decodeHex("content_digest", p.ContentDigest, sha256.Size)
	if err != nil {
		return Decision{Verdict: VerdictInvalid, Reason: "凭据内容摘要格式无效", Credential: cred}
	}
	sum := sha256.Sum256(content)
	if !bytes.Equal(sum[:], digest) {
		return Decision{Verdict: VerdictInvalid, Reason: "实际导出字节与凭据签名内容不一致：内容被改动或导出实现不同", Credential: cred}
	}
	if len(content) != p.ContentLength {
		return Decision{Verdict: VerdictInvalid, Reason: "导出内容长度与凭据声明不一致", Credential: cred}
	}
	if p.ContentType != "text/csv; charset=utf-8" || p.ExportVersion != "strata-comparison-csv-v1" {
		return Decision{Verdict: VerdictInvalid, Reason: "凭据绑定的导出格式与当前核验格式不一致", Credential: cred}
	}
	if err := VerifyCRLSignature(list, public, keyID); err != nil {
		return Decision{Verdict: VerdictInvalid, Reason: "吊销列表无法验证: " + err.Error(), Credential: cred}
	}
	// Revocation is recorded before expiry, so it takes precedence and gives
	// a verifier a definite "revoked" conclusion even past expiry.
	for _, entry := range list.Payload.Revocations {
		if entry.CredentialID == p.CredentialID {
			if entry.ComparisonID != "" && entry.ComparisonID != p.ComparisonID {
				return Decision{Verdict: VerdictInvalid, Reason: "吊销记录的对比结果编号与凭据不一致", Credential: cred}
			}
			at := entry.RevokedAt
			reason := entry.Reason
			return Decision{Verdict: VerdictRevoked, Reason: "凭据已被吊销", Credential: cred, RevokedAt: &at, RevokeReason: reason}
		}
	}
	now = now.UTC()
	if now.Add(leeway).Before(p.NotBefore) {
		return Decision{Verdict: VerdictInvalid, Reason: "凭据尚未生效", Credential: cred}
	}
	if !now.Add(-leeway).Before(p.ExpiresAt) {
		return Decision{Verdict: VerdictExpired, Reason: "凭据已过期，过期时间 " + p.ExpiresAt.Format(time.RFC3339), Credential: cred}
	}
	if now.Add(leeway).After(list.Payload.NextUpdate) {
		return Decision{Verdict: VerdictStaleCRL, Reason: "吊销列表已过期，无法离线确认凭据是否被吊销（next_update " + list.Payload.NextUpdate.Format(time.RFC3339) + "）", Credential: cred}
	}
	return Decision{Verdict: VerdictValid, Reason: "签名有效、内容字节一致、凭据在有效期内且未被吊销", Credential: cred}
}
