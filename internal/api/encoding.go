package api

import (
	"encoding/json"
	"errors"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/geology"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/persistence"
	"io"
	"log/slog"
	"mime"
	"net/http"
)

const bodyLimit = 4 << 20

func respond(w http.ResponseWriter, status int, value any) {
	data, err := json.Marshal(value)
	if err != nil {
		http.Error(w, "response encoding failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	w.Write(append(data, '\n'))
}

func decode(w http.ResponseWriter, r *http.Request, dst any) error {
	kind, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || kind != "application/json" {
		return &geology.Problem{Code: "media_type", Detail: "请求类型必须为 application/json"}
	}
	r.Body = http.MaxBytesReader(w, r.Body, bodyLimit)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(dst); err != nil {
		return decodeError(err)
	}
	var extra any
	if err = decoder.Decode(&extra); err != io.EOF {
		if err != nil {
			return decodeError(err)
		}
		return geology.Invalid("body", "只允许一个 JSON 对象")
	}
	return nil
}

func decodeError(err error) error {
	var size *http.MaxBytesError
	if errors.As(err, &size) {
		return &geology.Problem{Code: "too_large", Detail: "请求体超过 4 MiB"}
	}
	return geology.Invalid("body", "JSON 格式错误、字段未知或字段类型不正确")
}

func fail(w http.ResponseWriter, r *http.Request, err error, logger *slog.Logger) {
	if errors.Is(err, persistence.ErrReadOnly) {
		respond(w, http.StatusMethodNotAllowed, map[string]any{
			"error": geology.Problem{Code: "read_only", Detail: "数据目录以只读方式挂载，不能执行写入操作"},
		})
		return
	}
	var problem *geology.Problem
	if errors.As(err, &problem) {
		status := http.StatusUnprocessableEntity
		switch problem.Code {
		case "missing":
			status = http.StatusNotFound
		case "conflict":
			status = http.StatusConflict
		case "media_type":
			status = http.StatusUnsupportedMediaType
		case "too_large":
			status = http.StatusRequestEntityTooLarge
		}
		respond(w, status, map[string]any{"error": problem})
		return
	}
	logger.Error("request failed", "path", r.URL.Path, "error", err)
	respond(w, http.StatusInternalServerError, map[string]any{"error": geology.Problem{Code: "internal", Detail: "服务暂时无法完成请求"}})
}
