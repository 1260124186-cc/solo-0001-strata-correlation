package api

import (
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/catalog"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/geology"
	"net/http"
)

func (h *Handler) createInterpretation(w http.ResponseWriter, r *http.Request) {
	var input catalog.CreateInterpretation
	if err := decode(w, r, &input); err != nil {
		h.error(w, r, err)
		return
	}
	result, err := h.service.CreateInterpretation(r.Context(), input)
	if err != nil {
		h.error(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/v1/interpretations/"+result.ID)
	respond(w, http.StatusCreated, result)
}

func (h *Handler) reviseInterpretation(w http.ResponseWriter, r *http.Request) {
	var input catalog.ReviseInterpretation
	if err := decode(w, r, &input); err != nil {
		h.error(w, r, err)
		return
	}
	if input.ExpectedVersion < 1 {
		h.error(w, r, geology.Invalid("expected_version", "必须为正整数"))
		return
	}
	result, err := h.service.ReviseInterpretation(r.Context(), r.PathValue("id"), input)
	if err != nil {
		h.error(w, r, err)
		return
	}
	respond(w, http.StatusOK, result)
}

func (h *Handler) finalizeInterpretation(w http.ResponseWriter, r *http.Request) {
	var input catalog.StateChange
	if err := decode(w, r, &input); err != nil {
		h.error(w, r, err)
		return
	}
	if input.ExpectedVersion < 1 {
		h.error(w, r, geology.Invalid("expected_version", "必须为正整数"))
		return
	}
	result, err := h.service.FinalizeInterpretation(r.Context(), r.PathValue("id"), input)
	if err != nil {
		h.error(w, r, err)
		return
	}
	respond(w, http.StatusOK, result)
}

func (h *Handler) interpretation(w http.ResponseWriter, r *http.Request) {
	result, err := h.service.Interpretation(r.Context(), r.PathValue("id"))
	if err != nil {
		h.error(w, r, err)
		return
	}
	respond(w, http.StatusOK, result)
}

func (h *Handler) interpretationRevision(w http.ResponseWriter, r *http.Request) {
	version, err := integer(r.PathValue("version"), "version", 0, 1, 500)
	if err != nil {
		h.error(w, r, err)
		return
	}
	result, err := h.service.InterpretationRevision(r.Context(), r.PathValue("id"), version)
	if err != nil {
		h.error(w, r, err)
		return
	}
	respond(w, http.StatusOK, result)
}

func (h *Handler) interpretationHistory(w http.ResponseWriter, r *http.Request) {
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
	page, err := h.service.InterpretationHistory(r.Context(), r.PathValue("id"), offset, limit)
	if err != nil {
		h.error(w, r, err)
		return
	}
	respond(w, http.StatusOK, page)
}

func (h *Handler) interpretations(w http.ResponseWriter, r *http.Request) {
	q, err := query(r.URL.RawQuery, "profile_id", "state", "offset", "limit")
	if err != nil {
		h.error(w, r, err)
		return
	}
	offset, limit, err := pagination(q)
	if err != nil {
		h.error(w, r, err)
		return
	}
	page, err := h.service.Interpretations(r.Context(), q.Get("profile_id"), q.Get("state"), offset, limit)
	if err != nil {
		h.error(w, r, err)
		return
	}
	respond(w, http.StatusOK, page)
}
