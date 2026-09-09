package api

import (
	"bytes"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/correlation"
	"net/http"
)

func (h *Handler) compare(w http.ResponseWriter, r *http.Request) {
	var input correlation.Request
	if err := decode(w, r, &input); err != nil {
		h.error(w, r, err)
		return
	}
	result, reused, err := h.service.Compare(r.Context(), input)
	if err != nil {
		h.error(w, r, err)
		return
	}
	status := http.StatusCreated
	if reused {
		status = http.StatusOK
	}
	w.Header().Set("Location", "/api/v1/comparisons/"+result.ID)
	respond(w, status, result)
}

func (h *Handler) comparison(w http.ResponseWriter, r *http.Request) {
	result, err := h.service.Comparison(r.Context(), r.PathValue("id"))
	if err != nil {
		h.error(w, r, err)
		return
	}
	respond(w, http.StatusOK, result)
}

func (h *Handler) comparisons(w http.ResponseWriter, r *http.Request) {
	q, err := query(r.URL.RawQuery, "profile_id", "offset", "limit")
	if err != nil {
		h.error(w, r, err)
		return
	}
	offset, limit, err := pagination(q)
	if err != nil {
		h.error(w, r, err)
		return
	}
	page, err := h.service.Comparisons(r.Context(), q.Get("profile_id"), offset, limit)
	if err != nil {
		h.error(w, r, err)
		return
	}
	respond(w, http.StatusOK, page)
}

func (h *Handler) csv(w http.ResponseWriter, r *http.Request) {
	result, err := h.service.Comparison(r.Context(), r.PathValue("id"))
	if err != nil {
		h.error(w, r, err)
		return
	}
	var buf bytes.Buffer
	if err = correlation.WriteCSV(&buf, result); err != nil {
		h.error(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=\""+result.ID+".csv\"")
	w.WriteHeader(http.StatusOK)
	w.Write(buf.Bytes())
}
