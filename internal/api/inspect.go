package api

import (
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/correlation"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/geology"
	"net/http"
)

func (h *Handler) point(w http.ResponseWriter, r *http.Request) {
	q, err := query(r.URL.RawQuery, "depth_mm", "version")
	if err != nil {
		h.error(w, r, err)
		return
	}
	if q.Get("depth_mm") == "" {
		h.error(w, r, geology.Invalid("depth_mm", "必须提供查询深度"))
		return
	}
	depth, err := integer(q.Get("depth_mm"), "depth_mm", 0, 0, int(geology.MaxDepth))
	if err != nil {
		h.error(w, r, err)
		return
	}
	version, err := integer(q.Get("version"), "version", 0, 1, 500)
	if err != nil {
		h.error(w, r, err)
		return
	}
	result, err := h.service.Point(r.Context(), r.PathValue("id"), version, int64(depth))
	if err != nil {
		h.error(w, r, err)
		return
	}
	respond(w, http.StatusOK, result)
}

func (h *Handler) suggest(w http.ResponseWriter, r *http.Request) {
	var input correlation.OffsetRequest
	if err := decode(w, r, &input); err != nil {
		h.error(w, r, err)
		return
	}
	result, err := h.service.SuggestOffset(r.Context(), input)
	if err != nil {
		h.error(w, r, err)
		return
	}
	respond(w, http.StatusOK, result)
}

func (h *Handler) difference(w http.ResponseWriter, r *http.Request) {
	q, err := query(r.URL.RawQuery, "from", "to")
	if err != nil {
		h.error(w, r, err)
		return
	}
	if q.Get("from") == "" || q.Get("to") == "" {
		h.error(w, r, geology.Invalid("version", "必须提供 from 和 to"))
		return
	}
	from, err := integer(q.Get("from"), "from", 0, 1, 500)
	if err != nil {
		h.error(w, r, err)
		return
	}
	to, err := integer(q.Get("to"), "to", 0, 1, 500)
	if err != nil {
		h.error(w, r, err)
		return
	}
	result, err := h.service.Difference(r.Context(), r.PathValue("id"), from, to)
	if err != nil {
		h.error(w, r, err)
		return
	}
	respond(w, http.StatusOK, result)
}
