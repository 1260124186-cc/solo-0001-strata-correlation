package api

import (
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/catalog"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/geology"
	"net/http"
)

func (h *Handler) createAnnotation(w http.ResponseWriter, r *http.Request) {
	var input catalog.CreateAnnotation
	if err := decode(w, r, &input); err != nil {
		h.error(w, r, err)
		return
	}
	view, err := h.service.CreateAnnotation(r.Context(), input)
	if err != nil {
		h.error(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/v1/annotations/"+view.ID)
	respond(w, http.StatusCreated, view)
}

func (h *Handler) getAnnotation(w http.ResponseWriter, r *http.Request) {
	view, err := h.service.Annotation(r.Context(), r.PathValue("id"))
	if err != nil {
		h.error(w, r, err)
		return
	}
	respond(w, http.StatusOK, view)
}

func (h *Handler) listAnnotations(w http.ResponseWriter, r *http.Request) {
	q, err := query(r.URL.RawQuery, "profile_id", "status", "offset", "limit")
	if err != nil {
		h.error(w, r, err)
		return
	}
	offset, limit, err := pagination(q)
	if err != nil {
		h.error(w, r, err)
		return
	}
	page, err := h.service.Annotations(r.Context(), q.Get("profile_id"), geology.AnnotationStatus(q.Get("status")), offset, limit)
	if err != nil {
		h.error(w, r, err)
		return
	}
	respond(w, http.StatusOK, page)
}

func (h *Handler) reviseAnnotation(w http.ResponseWriter, r *http.Request) {
	var input catalog.ReviseAnnotation
	if err := decode(w, r, &input); err != nil {
		h.error(w, r, err)
		return
	}
	view, err := h.service.ReviseAnnotation(r.Context(), r.PathValue("id"), input)
	if err != nil {
		h.error(w, r, err)
		return
	}
	respond(w, http.StatusOK, view)
}

func (h *Handler) confirmAnnotation(w http.ResponseWriter, r *http.Request) {
	var input catalog.ConfirmAnnotation
	if err := decode(w, r, &input); err != nil {
		h.error(w, r, err)
		return
	}
	view, err := h.service.ConfirmAnnotation(r.Context(), r.PathValue("id"), input)
	if err != nil {
		h.error(w, r, err)
		return
	}
	respond(w, http.StatusOK, view)
}

func (h *Handler) reopenAnnotation(w http.ResponseWriter, r *http.Request) {
	var input catalog.ConfirmAnnotation
	if err := decode(w, r, &input); err != nil {
		h.error(w, r, err)
		return
	}
	view, err := h.service.ReopenAnnotation(r.Context(), r.PathValue("id"), input)
	if err != nil {
		h.error(w, r, err)
		return
	}
	respond(w, http.StatusOK, view)
}
