package api

import (
	"errors"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/catalog"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/geology"
	"net/http"
)

func (h *Handler) batch(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Items []catalog.BatchItem `json:"items"`
	}
	if err := decode(w, r, &input); err != nil {
		h.error(w, r, err)
		return
	}
	if len(input.Items) == 0 {
		h.error(w, r, geology.Invalid("items", "必须提供非空的修订数组"))
		return
	}
	if len(input.Items) > catalog.MaxBatchItems {
		h.error(w, r, geology.Invalid("items", "单次批量修订最多 100 项"))
		return
	}
	results, err := h.service.Batch(r.Context(), input.Items)
	if err != nil {
		var saveErr *catalog.SaveError
		if errors.As(err, &saveErr) {
			h.batchSaveFailure(w, r, results, saveErr)
			return
		}
		h.error(w, r, err)
		return
	}
	respond(w, http.StatusOK, map[string]any{"results": results})
}

// batchSaveFailure 在快照保存失败时仍返回逐项结果：committed 区分修订是否已
// 越过提交点生效，调用方据此决定安全重试还是重启后核对。
func (h *Handler) batchSaveFailure(w http.ResponseWriter, r *http.Request, results []catalog.BatchResult, saveErr *catalog.SaveError) {
	detail := "快照写入失败，本次批量修订未保存任何内容，修正原因后可安全重试"
	if saveErr.Committed {
		detail = "快照已提交但目录同步失败，标记成功的修订已生效；服务已停止后续写入，请检查磁盘并重启后核对，不要重放本批修订"
	}
	h.logger.Error("batch persist failed", "path", r.URL.Path, "committed", saveErr.Committed, "error", saveErr.Err)
	respond(w, http.StatusInternalServerError, map[string]any{
		"error":     &geology.Problem{Code: "persistence", Detail: detail},
		"committed": saveErr.Committed,
		"results":   results,
	})
}
