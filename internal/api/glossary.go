package api

import (
	"net/http"

	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/catalog"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/geology"
)

func (h *Handler) terms(w http.ResponseWriter, r *http.Request) {
	if _, err := query(r.URL.RawQuery); err != nil {
		h.error(w, r, err)
		return
	}
	items, err := h.service.Terms(r.Context())
	if err != nil {
		h.error(w, r, err)
		return
	}
	respond(w, http.StatusOK, map[string]any{"items": items})
}

func (h *Handler) createTerm(w http.ResponseWriter, r *http.Request) {
	var input catalog.TermInput
	if err := decode(w, r, &input); err != nil {
		h.error(w, r, err)
		return
	}
	term, err := h.service.AddTerm(r.Context(), input)
	if err != nil {
		h.error(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/v1/glossary/"+term.ID)
	respond(w, http.StatusCreated, term)
}

func (h *Handler) updateTerm(w http.ResponseWriter, r *http.Request) {
	var input catalog.TermInput
	if err := decode(w, r, &input); err != nil {
		h.error(w, r, err)
		return
	}
	if input.Enabled == nil {
		h.error(w, r, geology.Invalid("enabled", "更新时必须明确启用状态"))
		return
	}
	term, err := h.service.UpdateTerm(r.Context(), r.PathValue("id"), input)
	if err != nil {
		h.error(w, r, err)
		return
	}
	respond(w, http.StatusOK, term)
}

func (h *Handler) parseTerms(w http.ResponseWriter, r *http.Request) {
	var input catalog.ParseInput
	if err := decode(w, r, &input); err != nil {
		h.error(w, r, err)
		return
	}
	report, err := h.service.ParseTerms(r.Context(), input.Items)
	if err != nil {
		h.error(w, r, err)
		return
	}
	respond(w, http.StatusOK, report)
}
