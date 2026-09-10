package api

import (
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/correlation"
	"net/http"
)

func (h *Handler) createGroup(w http.ResponseWriter, r *http.Request) {
	var input correlation.GroupRequest
	if err := decode(w, r, &input); err != nil {
		h.error(w, r, err)
		return
	}
	group, err := h.service.CreateGroup(r.Context(), input)
	if err != nil {
		h.error(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/v1/comparison-groups/"+group.ID)
	respond(w, http.StatusAccepted, group)
}

func (h *Handler) group(w http.ResponseWriter, r *http.Request) {
	group, err := h.service.Group(r.Context(), r.PathValue("id"))
	if err != nil {
		h.error(w, r, err)
		return
	}
	respond(w, http.StatusOK, group)
}

func (h *Handler) groups(w http.ResponseWriter, r *http.Request) {
	q, err := query(r.URL.RawQuery, "offset", "limit")
	if err != nil {
		h.error(w, r, err)
		return
	}
	offset, limit, err := pagination(q)
	if err != nil {
		h.error(w, r, err)
		return
	}
	page, err := h.service.Groups(r.Context(), offset, limit)
	if err != nil {
		h.error(w, r, err)
		return
	}
	respond(w, http.StatusOK, page)
}

func (h *Handler) resumeGroup(w http.ResponseWriter, r *http.Request) {
	group, err := h.service.ResumeGroup(r.Context(), r.PathValue("id"))
	if err != nil {
		h.error(w, r, err)
		return
	}
	respond(w, http.StatusAccepted, group)
}
