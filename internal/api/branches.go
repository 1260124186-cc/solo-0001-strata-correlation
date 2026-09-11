package api

import (
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/catalog"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/geology"
	"net/http"
)

// fork 从任意历史版本固定分叉点，开启并行修订线。
func (h *Handler) fork(w http.ResponseWriter, r *http.Request) {
	var input catalog.ForkRequest
	if err := decode(w, r, &input); err != nil {
		h.error(w, r, err)
		return
	}
	if input.SourceVersion < 1 {
		h.error(w, r, geology.Invalid("source_version", "必须指定正整数来源版本"))
		return
	}
	result, err := h.service.Fork(r.Context(), r.PathValue("id"), input)
	if err != nil {
		h.error(w, r, err)
		return
	}
	profileResponse(w, http.StatusCreated, result.Revision.Profile)
}

func (h *Handler) branches(w http.ResponseWriter, r *http.Request) {
	views, err := h.service.Branches(r.Context(), r.PathValue("id"))
	if err != nil {
		h.error(w, r, err)
		return
	}
	respond(w, http.StatusOK, map[string]any{"items": views, "total": len(views)})
}

func (h *Handler) branchRelation(w http.ResponseWriter, r *http.Request) {
	relation, err := h.service.Relation(r.Context(), r.PathValue("id"), r.PathValue("branch"))
	if err != nil {
		h.error(w, r, err)
		return
	}
	respond(w, http.StatusOK, relation)
}

// mergePreview 只给出逐项差异与冲突，不写入任何版本。
func (h *Handler) mergePreview(w http.ResponseWriter, r *http.Request) {
	var input catalog.MergePreviewRequest
	if err := decode(w, r, &input); err != nil {
		h.error(w, r, err)
		return
	}
	preview, err := h.service.PreviewMerge(r.Context(), r.PathValue("id"), input)
	if err != nil {
		h.error(w, r, err)
		return
	}
	status := http.StatusOK
	if !preview.Mergeable {
		status = http.StatusConflict
	}
	respond(w, status, preview)
}

// merge 执行合并，产生新的主线版本而不覆盖任何已有版本。
func (h *Handler) merge(w http.ResponseWriter, r *http.Request) {
	var input catalog.MergeRequest
	if err := decode(w, r, &input); err != nil {
		h.error(w, r, err)
		return
	}
	if input.ExpectedVersion < 1 {
		h.error(w, r, geology.Invalid("expected_version", "必须为正整数"))
		return
	}
	result, err := h.service.Merge(r.Context(), r.PathValue("id"), input)
	if err != nil {
		h.error(w, r, err)
		return
	}
	profileResponse(w, http.StatusCreated, result.Revision.Profile)
}
