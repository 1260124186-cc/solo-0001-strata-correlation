package api

import (
	"net/http"

	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/catalog"
)

// POST /api/v1/profile-revisions/batch
func (h *Handler) batchRevise(w http.ResponseWriter, r *http.Request) {
	var input catalog.BatchInput
	if err := decode(w, r, &input); err != nil {
		h.error(w, r, err)
		return
	}
	view, err := h.service.BatchRevise(r.Context(), input)
	if err != nil {
		h.error(w, r, err)
		return
	}
	// 批次资源已经创建；逐条目成败见响应体 items，批次状态为
	// completed 或 interrupted，不因部分条目失败而变成错误响应。
	w.Header().Set("Location", "/api/v1/profile-revisions/batch/"+view.ID)
	respond(w, http.StatusCreated, view)
}

// GET /api/v1/profile-revisions/batch/{id}
func (h *Handler) batchStatus(w http.ResponseWriter, r *http.Request) {
	view, err := h.service.GetBatch(r.Context(), r.PathValue("id"))
	if err != nil {
		h.error(w, r, err)
		return
	}
	respond(w, http.StatusOK, view)
}
