package api

import (
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
		h.error(w, r, err)
		return
	}
	respond(w, http.StatusOK, map[string]any{"results": results})
}
