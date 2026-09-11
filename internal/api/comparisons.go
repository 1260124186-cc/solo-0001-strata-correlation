package api

import (
	"errors"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/correlation"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/geology"
	"io"
	"mime"
	"net/http"
)

func (h *Handler) compare(w http.ResponseWriter, r *http.Request) {
	var input correlation.Request
	if err := decode(w, r, &input); err != nil {
		h.error(w, r, err)
		return
	}
	result, reused, err := h.service.Compare(r.Context(), input)
	if err != nil {
		h.error(w, r, err)
		return
	}
	status := http.StatusCreated
	if reused {
		status = http.StatusOK
	}
	w.Header().Set("Location", "/api/v1/comparisons/"+result.ID)
	respond(w, status, result)
}

func (h *Handler) comparison(w http.ResponseWriter, r *http.Request) {
	result, err := h.service.Comparison(r.Context(), r.PathValue("id"))
	if err != nil {
		h.error(w, r, err)
		return
	}
	respond(w, http.StatusOK, result)
}

func (h *Handler) comparisons(w http.ResponseWriter, r *http.Request) {
	q, err := query(r.URL.RawQuery, "profile_id", "offset", "limit")
	if err != nil {
		h.error(w, r, err)
		return
	}
	offset, limit, err := pagination(q)
	if err != nil {
		h.error(w, r, err)
		return
	}
	page, err := h.service.Comparisons(r.Context(), q.Get("profile_id"), offset, limit)
	if err != nil {
		h.error(w, r, err)
		return
	}
	respond(w, http.StatusOK, page)
}

func (h *Handler) csv(w http.ResponseWriter, r *http.Request) {
	result, err := h.service.Comparison(r.Context(), r.PathValue("id"))
	if err != nil {
		h.error(w, r, err)
		return
	}
	content, err := correlation.CSVBytes(result)
	if err != nil {
		h.error(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=\""+result.ID+".csv\"")
	w.WriteHeader(http.StatusOK)
	w.Write(content)
}

func (h *Handler) manifest(w http.ResponseWriter, r *http.Request) {
	result, err := h.service.Comparison(r.Context(), r.PathValue("id"))
	if err != nil {
		h.error(w, r, err)
		return
	}
	manifest, err := correlation.NewManifest(result)
	if err != nil {
		h.error(w, r, err)
		return
	}
	respond(w, http.StatusOK, manifest)
}

// verifyCSV 只读校验已导出的 CSV：重新计算请求正文的摘要并与保存结果的
// 清单摘要比较，不修改任何状态。结果不存在返回 404，摘要不匹配返回 409。
func (h *Handler) verifyCSV(w http.ResponseWriter, r *http.Request) {
	kind, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || kind != "text/csv" {
		h.error(w, r, &geology.Problem{Code: "media_type", Detail: "请求类型必须为 text/csv"})
		return
	}
	result, err := h.service.Comparison(r.Context(), r.PathValue("id"))
	if err != nil {
		h.error(w, r, err)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, bodyLimit)
	content, err := io.ReadAll(r.Body)
	if err != nil {
		var size *http.MaxBytesError
		if errors.As(err, &size) {
			h.error(w, r, &geology.Problem{Code: "too_large", Detail: "请求体超过 4 MiB"})
			return
		}
		h.error(w, r, err)
		return
	}
	manifest, ok, err := correlation.VerifyCSV(result, content)
	if err != nil {
		h.error(w, r, err)
		return
	}
	if !ok {
		h.error(w, r, geology.Mismatch("CSV 内容与对比结果摘要不匹配"))
		return
	}
	respond(w, http.StatusOK, map[string]any{"valid": true, "manifest": manifest})
}
