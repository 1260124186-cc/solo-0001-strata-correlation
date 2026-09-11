package api

import (
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/catalog"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/geology"
	"net/http"
)

func (h *Handler) change(w http.ResponseWriter, r *http.Request, target geology.State) {
	var input catalog.StateChange
	if err := decode(w, r, &input); err != nil {
		h.error(w, r, err)
		return
	}
	if input.ExpectedVersion < 1 {
		h.error(w, r, geology.Invalid("expected_version", "必须为正整数"))
		return
	}
	if target == geology.Draft && input.Rules != nil {
		h.error(w, r, geology.Invalid("rules", "只有锁定请求才能指定完整性规则集合"))
		return
	}
	p, err := h.service.Change(r.Context(), r.PathValue("id"), target, input)
	if err != nil {
		h.error(w, r, err)
		return
	}
	profileResponse(w, http.StatusOK, p)
}

func (h *Handler) seal(w http.ResponseWriter, r *http.Request) {
	h.change(w, r, geology.Sealed)
}

func (h *Handler) reopen(w http.ResponseWriter, r *http.Request) {
	h.change(w, r, geology.Draft)
}

func (h *Handler) revision(w http.ResponseWriter, r *http.Request) {
	version, err := integer(r.PathValue("version"), "version", 0, 1, 500)
	if err != nil {
		h.error(w, r, err)
		return
	}
	revision, err := h.service.Revision(r.Context(), r.PathValue("id"), version)
	if err != nil {
		h.error(w, r, err)
		return
	}
	respond(w, http.StatusOK, revision)
}

func (h *Handler) integrity(w http.ResponseWriter, r *http.Request) {
	q, err := query(r.URL.RawQuery, "version")
	if err != nil {
		h.error(w, r, err)
		return
	}
	version, err := integer(q.Get("version"), "version", 0, 1, 500)
	if err != nil {
		h.error(w, r, err)
		return
	}
	explanation, err := h.service.Integrity(r.Context(), r.PathValue("id"), version)
	if err != nil {
		h.error(w, r, err)
		return
	}
	respond(w, http.StatusOK, explanation)
}

func (h *Handler) history(w http.ResponseWriter, r *http.Request) {
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
	page, err := h.service.History(r.Context(), r.PathValue("id"), offset, limit)
	if err != nil {
		h.error(w, r, err)
		return
	}
	respond(w, http.StatusOK, page)
}
