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
	view, reused, err := h.service.Compare(r.Context(), input)
	if err != nil {
		h.error(w, r, err)
		return
	}
	status := http.StatusCreated
	if reused {
		status = http.StatusOK
	}
	w.Header().Set("Location", "/api/v1/comparisons/"+view.ID)
	respond(w, status, view)
}

func (h *Handler) comparison(w http.ResponseWriter, r *http.Request) {
	view, err := h.service.Comparison(r.Context(), r.PathValue("id"))
	if err != nil {
		h.error(w, r, err)
		return
	}
	respond(w, http.StatusOK, view)
}

func (h *Handler) regenerate(w http.ResponseWriter, r *http.Request) {
	if r.ContentLength > 0 {
		var empty struct{}
		if err := decode(w, r, &empty); err != nil {
			h.error(w, r, err)
			return
		}
	}
	view, diff, reused, err := h.service.Regenerate(r.Context(), r.PathValue("id"))
	if err != nil {
		h.error(w, r, err)
		return
	}
	status := http.StatusCreated
	if reused {
		status = http.StatusOK
	}
	w.Header().Set("Location", "/api/v1/comparisons/"+view.ID)
	respond(w, status, map[string]any{"result": view, "diff": diff})
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
	view, err := h.service.Comparison(r.Context(), r.PathValue("id"))
	if err != nil {
		h.error(w, r, err)
		return
	}
	var buf bytes.Buffer
	if err = correlation.WriteCSV(&buf, view.Result); err != nil {
		h.error(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=\""+view.ID+".csv\"")
	w.WriteHeader(http.StatusOK)
	w.Write(buf.Bytes())
}
