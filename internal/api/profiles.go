package api

import (
	"fmt"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/catalog"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/geology"
	"net/http"
)

func profileResponse(w http.ResponseWriter, status int, p geology.Profile) {
	w.Header().Set("ETag", fmt.Sprintf("\"%s-v%d\"", p.ID, p.Version))
	respond(w, status, p)
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	var input geology.Metadata
	if err := decode(w, r, &input); err != nil {
		h.error(w, r, err)
		return
	}
	p, err := h.service.Create(r.Context(), input)
	if err != nil {
		h.error(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/v1/profiles/"+p.ID)
	profileResponse(w, http.StatusCreated, p)
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	p, err := h.service.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		h.error(w, r, err)
		return
	}
	profileResponse(w, http.StatusOK, p)
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	f, err := filter(r.URL.RawQuery)
	if err != nil {
		h.error(w, r, err)
		return
	}
	result, err := h.service.List(r.Context(), f)
	if err != nil {
		h.error(w, r, err)
		return
	}
	respond(w, http.StatusOK, result)
}

func (h *Handler) edit(w http.ResponseWriter, r *http.Request) {
	var input catalog.EditMetadata
	if err := decode(w, r, &input); err != nil {
		h.error(w, r, err)
		return
	}
	if input.ExpectedVersion < 1 {
		h.error(w, r, geology.Invalid("expected_version", "必须为正整数"))
		return
	}
	p, err := h.service.Edit(r.Context(), r.PathValue("id"), input)
	if err != nil {
		h.error(w, r, err)
		return
	}
	profileResponse(w, http.StatusOK, p)
}

func (h *Handler) replace(w http.ResponseWriter, r *http.Request) {
	var input catalog.ReplaceLayers
	if err := decode(w, r, &input); err != nil {
		h.error(w, r, err)
		return
	}
	if input.ExpectedVersion < 1 {
		h.error(w, r, geology.Invalid("expected_version", "必须为正整数"))
		return
	}
	p, err := h.service.Replace(r.Context(), r.PathValue("id"), input)
	if err != nil {
		h.error(w, r, err)
		return
	}
	profileResponse(w, http.StatusOK, p)
}

func (h *Handler) coverage(w http.ResponseWriter, r *http.Request) {
	p, err := h.service.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		h.error(w, r, err)
		return
	}
	respond(w, http.StatusOK, struct {
		Version  int              `json:"version"`
		Coverage geology.Coverage `json:"coverage"`
	}{p.Version, geology.CoverageOf(p)})
}
