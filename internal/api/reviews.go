package api

import (
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/catalog"
	"net/http"
)

func (h *Handler) createReview(w http.ResponseWriter, r *http.Request) {
	var input catalog.ReviewInput
	if err := decode(w, r, &input); err != nil {
		h.error(w, r, err)
		return
	}
	result, reused, err := h.service.Review(r.Context(), input)
	if err != nil {
		h.error(w, r, err)
		return
	}
	status := http.StatusCreated
	if reused {
		status = http.StatusOK
	}
	w.Header().Set("Location", "/api/v1/reviews/"+result.ID)
	respond(w, status, result)
}

func (h *Handler) review(w http.ResponseWriter, r *http.Request) {
	result, err := h.service.ReviewRecord(r.Context(), r.PathValue("id"))
	if err != nil {
		h.error(w, r, err)
		return
	}
	respond(w, http.StatusOK, result)
}

func (h *Handler) reviews(w http.ResponseWriter, r *http.Request) {
	q, err := query(r.URL.RawQuery, "profile_id", "version", "offset", "limit")
	if err != nil {
		h.error(w, r, err)
		return
	}
	offset, limit, err := pagination(q)
	if err != nil {
		h.error(w, r, err)
		return
	}
	version, err := integer(q.Get("version"), "version", 0, 0, 500)
	if err != nil {
		h.error(w, r, err)
		return
	}
	page, err := h.service.Reviews(r.Context(), q.Get("profile_id"), version, offset, limit)
	if err != nil {
		h.error(w, r, err)
		return
	}
	respond(w, http.StatusOK, page)
}
