package api

import (
	"fmt"
	"net/http"

	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/catalog"
)

func (h *Handler) derivePreview(w http.ResponseWriter, r *http.Request) {
	var input catalog.DerivePreviewRequest
	if err := decode(w, r, &input); err != nil {
		h.error(w, r, err)
		return
	}
	preview, err := h.service.PreviewDerivation(r.Context(), input)
	if err != nil {
		h.error(w, r, err)
		return
	}
	respond(w, http.StatusOK, preview)
}

func (h *Handler) deriveConfirm(w http.ResponseWriter, r *http.Request) {
	var input catalog.DeriveConfirmRequest
	if err := decode(w, r, &input); err != nil {
		h.error(w, r, err)
		return
	}
	result, err := h.service.ConfirmDerivation(r.Context(), input)
	if err != nil {
		h.error(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/v1/profiles/"+result.Profile.ID)
	w.Header().Set("ETag", fmt.Sprintf("\"%s-v%d\"", result.Profile.ID, result.Profile.Version))
	respond(w, http.StatusCreated, result)
}

func (h *Handler) derivation(w http.ResponseWriter, r *http.Request) {
	d, err := h.service.DerivationOf(r.Context(), r.PathValue("id"))
	if err != nil {
		h.error(w, r, err)
		return
	}
	respond(w, http.StatusOK, d)
}

func (h *Handler) lineage(w http.ResponseWriter, r *http.Request) {
	result, err := h.service.Lineage(r.Context(), r.PathValue("id"))
	if err != nil {
		h.error(w, r, err)
		return
	}
	respond(w, http.StatusOK, result)
}
