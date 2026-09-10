package api

import (
	"fmt"
	"net/http"

	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/catalog"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/collection"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/geology"
)

func collectionResponse(w http.ResponseWriter, status int, c collection.Collection) {
	w.Header().Set("ETag", fmt.Sprintf("\"%s-v%d\"", c.ID, c.Version))
	respond(w, status, c)
}

func (h *Handler) createCollection(w http.ResponseWriter, r *http.Request) {
	var input catalog.CollectionInput
	if err := decode(w, r, &input); err != nil {
		h.error(w, r, err)
		return
	}
	c, err := h.service.CreateCollection(r.Context(), input)
	if err != nil {
		h.error(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/v1/collections/"+c.ID)
	collectionResponse(w, http.StatusCreated, c)
}

func (h *Handler) getCollection(w http.ResponseWriter, r *http.Request) {
	c, err := h.service.Collection(r.Context(), r.PathValue("id"))
	if err != nil {
		h.error(w, r, err)
		return
	}
	collectionResponse(w, http.StatusOK, c)
}

func (h *Handler) listCollections(w http.ResponseWriter, r *http.Request) {
	q, err := query(r.URL.RawQuery, "q", "profile_id", "offset", "limit")
	if err != nil {
		h.error(w, r, err)
		return
	}
	offset, limit, err := pagination(q)
	if err != nil {
		h.error(w, r, err)
		return
	}
	page, err := h.service.Collections(r.Context(), q.Get("q"), q.Get("profile_id"), offset, limit)
	if err != nil {
		h.error(w, r, err)
		return
	}
	respond(w, http.StatusOK, page)
}

func (h *Handler) editCollection(w http.ResponseWriter, r *http.Request) {
	var input catalog.CollectionEdit
	if err := decode(w, r, &input); err != nil {
		h.error(w, r, err)
		return
	}
	if input.ExpectedVersion < 1 {
		h.error(w, r, geology.Invalid("expected_version", "必须为正整数"))
		return
	}
	c, err := h.service.EditCollection(r.Context(), r.PathValue("id"), input)
	if err != nil {
		h.error(w, r, err)
		return
	}
	collectionResponse(w, http.StatusOK, c)
}

func (h *Handler) deleteCollection(w http.ResponseWriter, r *http.Request) {
	var input catalog.CollectionDelete
	if err := decode(w, r, &input); err != nil {
		h.error(w, r, err)
		return
	}
	if input.ExpectedVersion < 1 {
		h.error(w, r, geology.Invalid("expected_version", "必须为正整数"))
		return
	}
	if err := h.service.DeleteCollection(r.Context(), r.PathValue("id"), input.ExpectedVersion); err != nil {
		h.error(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
