package catalog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/attest"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/correlation"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/geology"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/persistence"
	"strings"
	"time"
)

const (
	// maxCredentials bounds the signed credential table together with the
	// 10000 comparison result limit.
	maxCredentials = 10000
	// DefaultCredentialTTL is used when the issue request omits ttl_hours.
	DefaultCredentialTTL = 30 * 24 * time.Hour
)

// IssueResult returns the credential alongside the export bytes it covers, so
// the caller can hand both to the recipient without re-rendering the CSV.
type IssueResult struct {
	Credential attest.Credential `json:"credential"`
	Content    []byte            `json:"-"`
	Digest     string            `json:"digest"`
}

func sha256Hex(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

// IssueCredential signs the exact CSV export bytes of a stored comparison.
// Only one non-revoked, non-expired credential per comparison may be active; a
// new one can be issued once the active one has expired or been revoked.
func (s *Service) IssueCredential(ctx context.Context, comparisonID string, ttl time.Duration) (IssueResult, error) {
	var result IssueResult
	err := s.repo.Update(ctx, func(state *persistence.State) (bool, error) {
		comparison, exists := state.Comparisons[comparisonID]
		if !exists {
			return false, geology.Missing("对比结果不存在")
		}
		content, err := correlation.CSVBytes(comparison)
		if err != nil {
			return false, err
		}
		now := s.now()
		if active := activeCredential(state, comparisonID, now); active != nil {
			return false, geology.Conflict("该对比结果已有有效的未吊销凭据，过期或吊销后才能重新签发")
		}
		if len(state.Credentials) >= maxCredentials {
			return false, geology.Conflict("凭据数量达到 10000 条上限")
		}
		cred, err := s.issuer.NewCredential(comparisonID, correlation.ExportVersion, correlation.CSVContentType, content, now, ttl)
		if err != nil {
			return false, geology.Invalid("ttl_hours", err.Error())
		}
		state.Credentials[cred.Payload.CredentialID] = cred
		result = IssueResult{Credential: cred, Content: content, Digest: sha256Hex(content)}
		return true, nil
	})
	return result, err
}

// Credential returns a signed credential by id.
func (s *Service) Credential(ctx context.Context, id string) (attest.Credential, error) {
	var cred attest.Credential
	err := s.repo.View(ctx, func(state persistence.State) error {
		value, exists := state.Credentials[id]
		if !exists {
			return geology.Missing("凭据不存在")
		}
		cred = value
		return nil
	})
	return cred, err
}

// ActiveCredential returns the current valid credential for a comparison.
func (s *Service) ActiveCredential(ctx context.Context, comparisonID string) (attest.Credential, error) {
	var cred attest.Credential
	err := s.repo.View(ctx, func(state persistence.State) error {
		if _, exists := state.Comparisons[comparisonID]; !exists {
			return geology.Missing("对比结果不存在")
		}
		active := activeCredential(&state, comparisonID, s.now())
		if active == nil {
			return geology.Missing("该对比结果没有有效的未吊销凭据")
		}
		cred = *active
		return nil
	})
	return cred, err
}

// RevokeCredential publishes the credential id in a freshly signed CRL.
func (s *Service) RevokeCredential(ctx context.Context, id, reason string) (attest.CRL, error) {
	reason = strings.TrimSpace(reason)
	if err := geology.Text("reason", reason, 0, 500); err != nil {
		return attest.CRL{}, err
	}
	var list attest.CRL
	err := s.repo.Update(ctx, func(state *persistence.State) (bool, error) {
		cred, exists := state.Credentials[id]
		if !exists {
			return false, geology.Missing("凭据不存在")
		}
		for _, entry := range state.Revocations {
			if entry.CredentialID == id {
				return false, geology.Conflict("凭据已经被吊销")
			}
		}
		state.Revocations = append(state.Revocations, attest.Revocation{
			CredentialID: id,
			ComparisonID: cred.Payload.ComparisonID,
			RevokedAt:    s.now(),
			Reason:       reason,
		})
		state.CRLVersion = len(state.Revocations) + 1
		signed, err := s.signCRL(state)
		if err != nil {
			return false, err
		}
		state.CRL = signed
		list = signed
		return true, nil
	})
	return list, err
}

// CRL returns the signed revocation list, re-signing it first if its
// authoritative window is running out so an offline verifier always gets a
// current list.
func (s *Service) CRL(ctx context.Context) (attest.CRL, error) {
	var list attest.CRL
	err := s.repo.Update(ctx, func(state *persistence.State) (bool, error) {
		if crlFresh(state.CRL, s.now()) {
			list = state.CRL
			return false, nil
		}
		signed, err := s.signCRL(state)
		if err != nil {
			return false, err
		}
		state.CRL = signed
		list = signed
		return true, nil
	})
	return list, err
}

// BootstrapSignatures verifies every persisted signature against the loaded
// key and refreshes the revocation list. A mismatch or a tampered snapshot
// produces an error and prevents startup.
func (s *Service) BootstrapSignatures(ctx context.Context) error {
	return s.repo.Update(ctx, func(state *persistence.State) (bool, error) {
		for id, cred := range state.Credentials {
			if cred.Payload.IssuerKeyID != s.signer.KeyID {
				return false, fmt.Errorf("快照中的凭据 %s 由其他签发密钥 %s 签发，当前密钥 %s 无法核验，拒绝启动", id, cred.Payload.IssuerKeyID, s.signer.KeyID)
			}
			if err := attest.VerifyCredentialSignature(cred, s.signer.Public, s.signer.KeyID); err != nil {
				return false, fmt.Errorf("快照中的凭据 %s 签名核验失败: %w", id, err)
			}
		}
		if state.CRL.Signature != "" {
			if state.CRL.Payload.IssuerKeyID != s.signer.KeyID {
				return false, fmt.Errorf("快照中的吊销列表由其他签发密钥 %s 签发，当前密钥 %s 无法核验，拒绝启动", state.CRL.Payload.IssuerKeyID, s.signer.KeyID)
			}
			if err := attest.VerifyCRLSignature(state.CRL, s.signer.Public, s.signer.KeyID); err != nil {
				return false, fmt.Errorf("快照中的吊销列表签名核验失败: %w", err)
			}
		}
		// Always persist a signed, currently authoritative revocation list,
		// including the empty one: a verifier needs it even before the first
		// revocation, and an existing list is refreshed at startup.
		if state.CRLVersion == 0 {
			state.CRLVersion = len(state.Revocations) + 1
		}
		signed, err := s.signCRL(state)
		if err != nil {
			return false, err
		}
		state.CRL = signed
		return true, nil
	})
}

// signCRL rebuilds the signed list. The version is derived from the number of
// revocation entries; time-only refreshes keep the same version so a verifier
// still treats the list as a consistent continuation.
func (s *Service) signCRL(state *persistence.State) (attest.CRL, error) {
	version := state.CRLVersion
	if version == 0 {
		version = len(state.Revocations) + 1
	}
	return s.issuer.SignCRL(version, state.Revocations, s.now())
}

func activeCredential(state *persistence.State, comparisonID string, now time.Time) *attest.Credential {
	revoked := map[string]bool{}
	for _, entry := range state.Revocations {
		revoked[entry.CredentialID] = true
	}
	var active *attest.Credential
	for id, cred := range state.Credentials {
		if cred.Payload.ComparisonID != comparisonID || revoked[id] {
			continue
		}
		if !now.Before(cred.Payload.ExpiresAt) {
			continue
		}
		if active != nil && !cred.Payload.IssuedAt.After(active.Payload.IssuedAt) {
			continue
		}
		candidate := cred
		active = &candidate
	}
	return active
}

func crlFresh(list attest.CRL, now time.Time) bool {
	if list.Signature == "" {
		return false
	}
	// Refresh once half the window has elapsed, leaving verifiers ample
	// offline time before NextUpdate.
	return now.Before(list.Payload.ThisUpdate.Add(attest.CRLNextUpdateWindow / 2))
}
