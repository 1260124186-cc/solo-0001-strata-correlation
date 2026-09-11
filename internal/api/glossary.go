package api

import (
	"net/http"

	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/catalog"
)

func (h *Handler) mergeGlossary(w http.ResponseWriter, r *http.Request) {
	var input catalog.GlossaryMerge
	if err := decode(w, r, &input); err != nil {
		h.error(w, r, err)
		return
	}
	result, err := h.service.MergeGlossary(r.Context(), input)
	if err != nil {
		h.error(w, r, err)
		return
	}
	respond(w, http.StatusOK, result)
}

func (h *Handler) glossary(w http.ResponseWriter, r *http.Request) {
	q, err := query(r.URL.RawQuery, "q", "offset", "limit")
	if err != nil {
		h.error(w, r, err)
		return
	}
	offset, limit, err := pagination(q)
	if err != nil {
		h.error(w, r, err)
		return
	}
	page, err := h.service.ListGlossary(r.Context(), q.Get("q"), offset, limit)
	if err != nil {
		h.error(w, r, err)
		return
	}
	respond(w, http.StatusOK, page)
}

func (h *Handler) legacyMarkers(w http.ResponseWriter, r *http.Request) {
	report, err := h.service.LegacyMarkerReport(r.Context())
	if err != nil {
		h.error(w, r, err)
		return
	}
	respond(w, http.StatusOK, report)
}
