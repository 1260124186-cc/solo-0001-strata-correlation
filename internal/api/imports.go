package api

import (
	"errors"
	"io"
	"mime"
	"net/http"

	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/catalog"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/geology"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/importing"
)

func readCSVBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	kind, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || (kind != "text/csv" && kind != "application/csv") {
		respond(w, http.StatusUnsupportedMediaType, map[string]any{
			"error": geology.Problem{Code: "media_type", Detail: "请求类型必须为 text/csv"},
		})
		return nil, false
	}
	r.Body = http.MaxBytesReader(w, r.Body, bodyLimit)
	data, err := io.ReadAll(r.Body)
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		respond(w, http.StatusRequestEntityTooLarge, map[string]any{
			"error": geology.Problem{Code: "too_large", Detail: "请求体超过 4 MiB"},
		})
		return nil, false
	}
	if err != nil {
		respond(w, http.StatusInternalServerError, map[string]any{
			"error": geology.Problem{Code: "internal", Detail: "服务暂时无法完成请求"},
		})
		return nil, false
	}
	return data, true
}

func (h *Handler) createImport(w http.ResponseWriter, r *http.Request) {
	source, ok := readCSVBody(w, r)
	if !ok {
		return
	}
	job, reused, err := h.service.CreateImport(r.Context(), source)
	if err != nil {
		var structure *importing.StructureError
		if errors.As(err, &structure) {
			h.error(w, r, geology.Invalid("csv", structure.Detail))
			return
		}
		h.error(w, r, err)
		return
	}
	status := http.StatusCreated
	if reused {
		status = http.StatusOK
	}
	w.Header().Set("Location", "/api/v1/profile-imports/"+job.ID)
	respond(w, status, job.View())
}

func (h *Handler) getImport(w http.ResponseWriter, r *http.Request) {
	job, err := h.service.GetImport(r.Context(), r.PathValue("id"))
	if err != nil {
		h.error(w, r, err)
		return
	}
	respond(w, http.StatusOK, job.View())
}

func (h *Handler) confirmImport(w http.ResponseWriter, r *http.Request) {
	// The reason is optional; an empty body uses a default audit reason.
	input := struct {
		Reason string `json:"reason"`
	}{Reason: "确认 CSV 批量导入"}
	kind, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if r.ContentLength > 0 {
		if kind != "application/json" {
			h.error(w, r, &geology.Problem{Code: "media_type", Detail: "请求类型必须为 application/json，或留空请求体"})
			return
		}
		if err := decode(w, r, &input); err != nil {
			h.error(w, r, err)
			return
		}
		if input.Reason == "" {
			input.Reason = "确认 CSV 批量导入"
		}
	} else if kind != "" && kind != "application/json" {
		h.error(w, r, &geology.Problem{Code: "media_type", Detail: "请求类型必须为 application/json，或留空请求体"})
		return
	}
	job, err := h.service.ConfirmImport(r.Context(), r.PathValue("id"), input.Reason)
	if err != nil {
		var rejected *catalog.ImportRejected
		if errors.As(err, &rejected) {
			// 422 body carries both the standard error envelope and the failed
			// preview so callers see line-numbered problems immediately.
			respond(w, http.StatusUnprocessableEntity, map[string]any{
				"error":  geology.Problem{Code: "invalid", Detail: rejected.Import.Failure},
				"import": rejected.Import.View(),
			})
			return
		}
		h.error(w, r, err)
		return
	}
	respond(w, http.StatusOK, job.View())
}
