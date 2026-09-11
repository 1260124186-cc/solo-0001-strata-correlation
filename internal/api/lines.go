package api

import (
	"fmt"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/catalog"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/geology"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/line"
	"net/http"
)

func etagForLine(view line.LineView) string {
	return fmt.Sprintf("\"%s-r%d\"", view.ID, view.Revision)
}

func (h *Handler) createLine(w http.ResponseWriter, r *http.Request) {
	var input catalog.CreateLine
	if err := decode(w, r, &input); err != nil {
		h.error(w, r, err)
		return
	}
	l, err := h.service.CreateLine(r.Context(), input)
	if err != nil {
		h.error(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/v1/lines/"+l.ID)
	lineResponse(w, http.StatusCreated, h, r, l.ID)
}

func (h *Handler) getLine(w http.ResponseWriter, r *http.Request) {
	view, err := h.service.Line(r.Context(), r.PathValue("id"))
	if err != nil {
		h.error(w, r, err)
		return
	}
	w.Header().Set("ETag", etagForLine(view))
	respond(w, http.StatusOK, view)
}

func (h *Handler) adjacent(w http.ResponseWriter, r *http.Request) {
	result, err := h.service.Adjacent(r.Context(), r.PathValue("id"), r.PathValue("profile"))
	if err != nil {
		h.error(w, r, err)
		return
	}
	respond(w, http.StatusOK, result)
}

func (h *Handler) listLines(w http.ResponseWriter, r *http.Request) {
	q, err := query(r.URL.RawQuery, "q", "state", "offset", "limit")
	if err != nil {
		h.error(w, r, err)
		return
	}
	offset, limit, err := pagination(q)
	if err != nil {
		h.error(w, r, err)
		return
	}
	page, err := h.service.Lines(r.Context(), q.Get("q"), line.State(q.Get("state")), offset, limit)
	if err != nil {
		h.error(w, r, err)
		return
	}
	respond(w, http.StatusOK, page)
}

func (h *Handler) editLineMeta(w http.ResponseWriter, r *http.Request) {
	var input catalog.LineMeta
	if err := decode(w, r, &input); err != nil {
		h.error(w, r, err)
		return
	}
	if input.ExpectedRevision < 1 {
		h.error(w, r, geology.Invalid("expected_revision", "必须为正整数"))
		return
	}
	l, err := h.service.EditLineMeta(r.Context(), r.PathValue("id"), input)
	if err != nil {
		h.error(w, r, err)
		return
	}
	lineResponse(w, http.StatusOK, h, r, l.ID)
}

func (h *Handler) replaceLineStations(w http.ResponseWriter, r *http.Request) {
	var input catalog.ReplaceLineStations
	if err := decode(w, r, &input); err != nil {
		h.error(w, r, err)
		return
	}
	if input.ExpectedRevision < 1 {
		h.error(w, r, geology.Invalid("expected_revision", "必须为正整数"))
		return
	}
	l, err := h.service.ReplaceLineStations(r.Context(), r.PathValue("id"), input)
	if err != nil {
		h.error(w, r, err)
		return
	}
	lineResponse(w, http.StatusOK, h, r, l.ID)
}

func (h *Handler) reorderLine(w http.ResponseWriter, r *http.Request) {
	var input catalog.ReorderLine
	if err := decode(w, r, &input); err != nil {
		h.error(w, r, err)
		return
	}
	if input.ExpectedRevision < 1 {
		h.error(w, r, geology.Invalid("expected_revision", "必须为正整数"))
		return
	}
	l, err := h.service.ReorderLine(r.Context(), r.PathValue("id"), input)
	if err != nil {
		h.error(w, r, err)
		return
	}
	lineResponse(w, http.StatusOK, h, r, l.ID)
}

func (h *Handler) finalizeLine(w http.ResponseWriter, r *http.Request) {
	var input catalog.FinalizeLine
	if err := decode(w, r, &input); err != nil {
		h.error(w, r, err)
		return
	}
	if input.ExpectedRevision < 1 {
		h.error(w, r, geology.Invalid("expected_revision", "必须为正整数"))
		return
	}
	l, err := h.service.FinalizeLine(r.Context(), r.PathValue("id"), input)
	if err != nil {
		h.error(w, r, err)
		return
	}
	lineResponse(w, http.StatusOK, h, r, l.ID)
}

func (h *Handler) forkLine(w http.ResponseWriter, r *http.Request) {
	var input catalog.ForkLine
	if err := decode(w, r, &input); err != nil {
		h.error(w, r, err)
		return
	}
	l, err := h.service.ForkLine(r.Context(), r.PathValue("id"), input)
	if err != nil {
		h.error(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/v1/lines/"+l.ID)
	lineResponse(w, http.StatusCreated, h, r, l.ID)
}

// lineResponse 在每次成功写入后重新读取规范化视图，保证返回的相邻顺序是稳定表达。
func lineResponse(w http.ResponseWriter, status int, h *Handler, r *http.Request, id string) {
	view, err := h.service.Line(r.Context(), id)
	if err != nil {
		h.error(w, r, err)
		return
	}
	w.Header().Set("ETag", etagForLine(view))
	respond(w, status, view)
}
