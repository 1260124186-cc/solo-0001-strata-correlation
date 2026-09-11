package persistence

import (
	"encoding/json"
	"fmt"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/attest"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/correlation"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/geology"
	"reflect"
	"time"
)

type State struct {
	Schema      int                           `json:"schema"`
	Histories   map[string][]geology.Revision `json:"histories"`
	Comparisons map[string]correlation.Result `json:"comparisons"`
	// Credentials maps credential id to the signed export credential.
	Credentials map[string]attest.Credential `json:"credentials"`
	// Revocations is the append-only source list reflected by the signed CRL.
	Revocations []attest.Revocation `json:"revocations"`
	// CRLVersion advances only when a revocation entry is appended; time-only
	// CRL refreshes keep the version stable.
	CRLVersion int `json:"crl_version"`
	// CRL is the signed revocation list mirroring Revocations.
	CRL attest.CRL `json:"crl"`
}

const currentSchema = 2

func emptyState() State {
	return State{
		Schema:      currentSchema,
		Histories:   map[string][]geology.Revision{},
		Comparisons: map[string]correlation.Result{},
		Credentials: map[string]attest.Credential{},
		Revocations: []attest.Revocation{},
	}
}

func (s State) Clone() State {
	out := emptyState()
	for id, revisions := range s.Histories {
		copies := make([]geology.Revision, len(revisions))
		for i, r := range revisions {
			copies[i] = r.Clone()
		}
		out.Histories[id] = copies
	}
	for id, result := range s.Comparisons {
		out.Comparisons[id] = result.Clone()
	}
	for id, credential := range s.Credentials {
		out.Credentials[id] = cloneCredential(credential)
	}
	out.Revocations = append(out.Revocations, s.Revocations...)
	out.CRLVersion = s.CRLVersion
	out.CRL = s.CRL
	return out
}

func cloneCredential(cred attest.Credential) attest.Credential {
	return cred
}

func (s State) Latest(id string) (geology.Profile, error) {
	history, ok := s.Histories[id]
	if !ok || len(history) == 0 {
		return geology.Profile{}, geology.Missing("剖面不存在")
	}
	return history[len(history)-1].Profile.Clone(), nil
}

func (s State) Revision(id string, version int) (geology.Revision, error) {
	history, ok := s.Histories[id]
	if !ok {
		return geology.Revision{}, geology.Missing("剖面不存在")
	}
	if version < 1 || version > len(history) {
		return geology.Revision{}, geology.Missing("历史版本不存在")
	}
	return history[version-1].Clone(), nil
}

func (s State) Validate() error {
	if s.Schema != currentSchema || s.Histories == nil || s.Comparisons == nil || s.Credentials == nil || s.Revocations == nil {
		return fmt.Errorf("unsupported snapshot shape")
	}
	for id, history := range s.Histories {
		if len(history) == 0 {
			return fmt.Errorf("empty history %s", id)
		}
		for i, r := range history {
			if r.Profile.ID != id || r.Profile.Version != i+1 || r.Event.Version != i+1 || !r.Event.At.Equal(r.Profile.UpdatedAt) {
				return fmt.Errorf("inconsistent revision %s/%d", id, i+1)
			}
			if err := r.Profile.Validate(); err != nil {
				return fmt.Errorf("invalid revision %s: %w", id, err)
			}
			if err := geology.Text("reason", r.Event.Reason, 1, 500); err != nil {
				return err
			}
			if i == 0 {
				if r.Event.Action != "create" || r.Profile.State != geology.Draft {
					return fmt.Errorf("invalid initial revision")
				}
			} else {
				before := history[i-1].Profile
				if !before.CreatedAt.Equal(r.Profile.CreatedAt) || r.Profile.UpdatedAt.Before(before.UpdatedAt) {
					return fmt.Errorf("invalid revision chronology")
				}
				if err := validateStep(before, r); err != nil {
					return err
				}
			}
		}
	}
	for id, result := range s.Comparisons {
		if id != result.ID || id != result.Request.Key() || result.Algorithm != correlation.Algorithm || result.CreatedAt.IsZero() {
			return fmt.Errorf("invalid comparison identity")
		}
		a, err := s.Revision(result.Request.Left.ID, result.Request.Left.Version)
		if err != nil {
			return err
		}
		b, err := s.Revision(result.Request.Right.ID, result.Request.Right.Version)
		if err != nil {
			return err
		}
		computed, err := correlation.Align(a.Profile, b.Profile, result.Request, result.CreatedAt)
		if err != nil {
			return err
		}
		expected, _ := json.Marshal(computed)
		actual, _ := json.Marshal(result)
		if string(expected) != string(actual) {
			return fmt.Errorf("comparison data mismatch")
		}
	}
	if err := s.validateCredentials(); err != nil {
		return err
	}
	return s.validateRevocations()
}

// validateCredentials checks every credential structurally and re-derives its
// binding to the exact CSV export bytes. Signature verification against the
// configured key happens separately in VerifySignatures.
func (s State) validateCredentials() error {
	for id, cred := range s.Credentials {
		p := cred.Payload
		if id != p.CredentialID {
			return fmt.Errorf("invalid credential identity %s", id)
		}
		if !validAttID(id) {
			return fmt.Errorf("invalid credential id %s", id)
		}
		if p.Version != 1 || p.Kind != "strata-comparison-credential" || p.IssuerKeyID == "" {
			return fmt.Errorf("invalid credential shape %s", id)
		}
		result, exists := s.Comparisons[p.ComparisonID]
		if !exists {
			return fmt.Errorf("credential %s references missing comparison", id)
		}
		digest, content, err := correlation.ContentDigest(result)
		if err != nil {
			return err
		}
		if p.ContentDigest != digest || p.ContentLength != len(content) {
			return fmt.Errorf("credential %s does not cover the current export bytes", id)
		}
		if p.ExportVersion != correlation.ExportVersion || p.ContentType != correlation.CSVContentType {
			return fmt.Errorf("credential %s binds an unknown export format", id)
		}
		if p.IssuedAt.IsZero() || !p.IssuedAt.Equal(p.NotBefore) || !p.ExpiresAt.After(p.IssuedAt) {
			return fmt.Errorf("credential %s has invalid validity window", id)
		}
		if want := attest.CredentialID(p.ComparisonID, content, p.IssuedAt); want != id {
			return fmt.Errorf("credential %s identity does not match signed content", id)
		}
		if cred.Signature == "" {
			return fmt.Errorf("credential %s is not signed", id)
		}
	}
	return nil
}

func (s State) validateRevocations() error {
	seen := map[string]bool{}
	var previous time.Time
	for i, entry := range s.Revocations {
		if !validAttID(entry.CredentialID) {
			return fmt.Errorf("invalid revocation id at %d", i)
		}
		if seen[entry.CredentialID] {
			return fmt.Errorf("credential %s revoked twice", entry.CredentialID)
		}
		seen[entry.CredentialID] = true
		cred, exists := s.Credentials[entry.CredentialID]
		if !exists {
			return fmt.Errorf("revocation references missing credential %s", entry.CredentialID)
		}
		if entry.ComparisonID != cred.Payload.ComparisonID {
			return fmt.Errorf("revocation comparison mismatch for %s", entry.CredentialID)
		}
		if entry.RevokedAt.IsZero() {
			return fmt.Errorf("revocation for %s has no timestamp", entry.CredentialID)
		}
		if i > 0 && !entry.RevokedAt.After(previous) {
			return fmt.Errorf("revocation timestamps must be strictly increasing")
		}
		previous = entry.RevokedAt
	}
	if s.CRL.Signature == "" {
		if len(s.Revocations) != 0 {
			return fmt.Errorf("revocations exist without a signed list")
		}
		return nil
	}
	p := s.CRL.Payload
	if p.Version < 1 || p.Kind != "strata-revocation-list" || p.IssuerKeyID == "" {
		return fmt.Errorf("invalid revocation list shape")
	}
	if s.CRLVersion != p.Version || s.CRLVersion != len(s.Revocations)+1 {
		return fmt.Errorf("revocation list version is out of sync")
	}
	if !p.NextUpdate.Equal(p.ThisUpdate.Add(attest.CRLNextUpdateWindow)) {
		return fmt.Errorf("revocation list window has been altered")
	}
	if len(p.Revocations) != len(s.Revocations) {
		return fmt.Errorf("signed revocation list is out of sync")
	}
	for i, entry := range s.Revocations {
		if !reflect.DeepEqual(entry, p.Revocations[i]) {
			return fmt.Errorf("signed revocation list is out of sync")
		}
	}
	return nil
}

func validAttID(id string) bool {
	const prefix = "att_"
	if len(id) != len(prefix)+32 || id[:len(prefix)] != prefix {
		return false
	}
	for _, c := range id[len(prefix):] {
		if !(c >= '0' && c <= '9') && !(c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func validateStep(before geology.Profile, r geology.Revision) error {
	after := r.Profile
	switch r.Event.Action {
	case "metadata", "layers":
		if before.State != geology.Draft || after.State != geology.Draft {
			return fmt.Errorf("edited sealed revision")
		}
		if r.Event.Action == "layers" && before.Metadata != after.Metadata {
			return fmt.Errorf("layers edit changed metadata")
		}
		if r.Event.Action == "metadata" && !reflect.DeepEqual(before.Layers, after.Layers) {
			return fmt.Errorf("metadata edit changed layers")
		}
	case "seal", "reopen":
		expected := geology.Sealed
		if r.Event.Action == "reopen" {
			expected = geology.Draft
		}
		if after.State != expected || before.State == expected || before.Metadata != after.Metadata || !reflect.DeepEqual(before.Layers, after.Layers) {
			return fmt.Errorf("invalid state change")
		}
	default:
		return fmt.Errorf("unknown revision action")
	}
	return nil
}
