package api

import (
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/catalog"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/correlation"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/geology"
	"net/http"
	"time"
)

type issueBody struct {
	TTLHours int `json:"ttl_hours,omitempty"`
}

type revokeBody struct {
	Reason string `json:"reason"`
}

// issueCredential signs the exact CSV bytes of a comparison. The response is
// the credential plus the single canonical CSV rendering, so the recipient can
// verify the downloaded bytes against the signature without any second export
// path.
func (h *Handler) issueCredential(w http.ResponseWriter, r *http.Request) {
	var body issueBody
	if err := decode(w, r, &body); err != nil {
		h.error(w, r, err)
		return
	}
	ttl := catalog.DefaultCredentialTTL
	if body.TTLHours != 0 {
		if body.TTLHours < 1 || body.TTLHours > 90*24 {
			h.error(w, r, geology.Invalid("ttl_hours", "有效期必须为 1 到 2160 小时"))
			return
		}
		ttl = time.Duration(body.TTLHours) * time.Hour
	}
	result, err := h.service.IssueCredential(r.Context(), r.PathValue("id"), ttl)
	if err != nil {
		h.error(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/v1/credentials/"+result.Credential.Payload.CredentialID)
	respond(w, http.StatusCreated, map[string]any{
		"credential":     result.Credential,
		"content_type":   correlation.CSVContentType,
		"digest":         result.Digest,
		"content_length": len(result.Content),
	})
}

func (h *Handler) getCredential(w http.ResponseWriter, r *http.Request) {
	cred, err := h.service.Credential(r.Context(), r.PathValue("id"))
	if err != nil {
		h.error(w, r, err)
		return
	}
	respond(w, http.StatusOK, cred)
}

func (h *Handler) comparisonCredential(w http.ResponseWriter, r *http.Request) {
	cred, err := h.service.ActiveCredential(r.Context(), r.PathValue("id"))
	if err != nil {
		h.error(w, r, err)
		return
	}
	respond(w, http.StatusOK, cred)
}

func (h *Handler) revokeCredential(w http.ResponseWriter, r *http.Request) {
	var body revokeBody
	if err := decode(w, r, &body); err != nil {
		h.error(w, r, err)
		return
	}
	list, err := h.service.RevokeCredential(r.Context(), r.PathValue("id"), body.Reason)
	if err != nil {
		h.error(w, r, err)
		return
	}
	respond(w, http.StatusOK, map[string]any{"crl": list})
}

func (h *Handler) crl(w http.ResponseWriter, r *http.Request) {
	list, err := h.service.CRL(r.Context())
	if err != nil {
		h.error(w, r, err)
		return
	}
	respond(w, http.StatusOK, list)
}

func (h *Handler) signingKey(w http.ResponseWriter, r *http.Request) {
	pem, err := h.service.SignerKey().PublicKeyPEM()
	if err != nil {
		h.error(w, r, err)
		return
	}
	respond(w, http.StatusOK, map[string]any{
		"key_id":         h.service.SignerKey().KeyID,
		"algorithm":      "Ed25519",
		"public_key_pem": string(pem),
	})
}
