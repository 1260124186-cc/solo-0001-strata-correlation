package api

import (
	"bytes"
	"net/http"

	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/correlation"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/geology"
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

func (h *Handler) parseRange(r *http.Request) (int, int64, int64, error) {
	q, err := query(r.URL.RawQuery, "from_mm", "to_mm", "version")
	if err != nil {
		return 0, 0, 0, err
	}
	if q.Get("from_mm") == "" || q.Get("to_mm") == "" {
		return 0, 0, 0, geology.Invalid("range", "必须提供非空 from_mm 和 to_mm")
	}
	from, err := integer64(q.Get("from_mm"), "from_mm", 0, geology.MaxDepth)
	if err != nil {
		return 0, 0, 0, err
	}
	to, err := integer64(q.Get("to_mm"), "to_mm", 1, geology.MaxDepth)
	if err != nil {
		return 0, 0, 0, err
	}
	version, err := integer(q.Get("version"), "version", 0, 1, 500)
	if err != nil {
		return 0, 0, 0, err
	}
	return version, from, to, nil
}

func (h *Handler) rangeQuery(w http.ResponseWriter, r *http.Request) {
	version, from, to, err := h.parseRange(r)
	if err != nil {
		h.error(w, r, err)
		return
	}
	result, err := h.service.Range(r.Context(), r.PathValue("id"), version, from, to)
	if err != nil {
		h.error(w, r, err)
		return
	}
	respond(w, http.StatusOK, result)
}

func (h *Handler) rangeCSV(w http.ResponseWriter, r *http.Request) {
	version, from, to, err := h.parseRange(r)
	if err != nil {
		h.error(w, r, err)
		return
	}
	result, err := h.service.Range(r.Context(), r.PathValue("id"), version, from, to)
	if err != nil {
		h.error(w, r, err)
		return
	}
	var buf bytes.Buffer
	if err = geology.WriteRangeCSV(&buf, result); err != nil {
		h.error(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=\""+result.ProfileID+"-range.csv\"")
	w.WriteHeader(http.StatusOK)
	w.Write(buf.Bytes())
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
