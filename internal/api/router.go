package api

import (
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/catalog"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/geology"
	"log/slog"
	"net/http"
)

type Handler struct {
	service  *catalog.Service
	logger   *slog.Logger
	readOnly bool
}

func New(service *catalog.Service, logger *slog.Logger, readOnly bool) http.Handler {
	h := &Handler{service: service, logger: logger, readOnly: readOnly}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", h.health)
	mux.HandleFunc("GET /api/v1/rocks", h.rocks)
	mux.HandleFunc("POST /api/v1/profiles", h.writeOnly(h.create))
	mux.HandleFunc("GET /api/v1/profiles", h.list)
	mux.HandleFunc("GET /api/v1/profiles/{id}", h.get)
	mux.HandleFunc("PUT /api/v1/profiles/{id}", h.writeOnly(h.edit))
	mux.HandleFunc("PUT /api/v1/profiles/{id}/layers", h.writeOnly(h.replace))
	mux.HandleFunc("GET /api/v1/profiles/{id}/coverage", h.coverage)
	mux.HandleFunc("GET /api/v1/profiles/{id}/at", h.point)
	mux.HandleFunc("GET /api/v1/profiles/{id}/diff", h.difference)
	mux.HandleFunc("POST /api/v1/comparison-offsets", h.suggest)
	mux.HandleFunc("POST /api/v1/profiles/{id}/seal", h.writeOnly(h.seal))
	mux.HandleFunc("POST /api/v1/profiles/{id}/reopen", h.writeOnly(h.reopen))
	mux.HandleFunc("GET /api/v1/profiles/{id}/history", h.history)
	mux.HandleFunc("GET /api/v1/profiles/{id}/revisions/{version}", h.revision)
	mux.HandleFunc("POST /api/v1/comparisons", h.writeOnly(h.compare))
	mux.HandleFunc("GET /api/v1/comparisons", h.comparisons)
	mux.HandleFunc("GET /api/v1/comparisons/{id}", h.comparison)
	mux.HandleFunc("GET /api/v1/comparisons/{id}/csv", h.csv)
	return middleware(mux, logger)
}

func (h *Handler) error(w http.ResponseWriter, r *http.Request, err error) {
	fail(w, r, err, h.logger)
}

func (h *Handler) writeOnly(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if h.readOnly {
			respond(w, http.StatusMethodNotAllowed, map[string]any{
				"error": geology.Problem{Code: "read_only", Detail: "数据目录以只读方式挂载，不能执行写入操作"},
			})
			return
		}
		next(w, r)
	}
}

func (h *Handler) health(w http.ResponseWriter, r *http.Request) {
	if h.service.Health() != nil {
		respond(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable"})
		return
	}
	respond(w, http.StatusOK, map[string]string{"status": "ready"})
}

func (h *Handler) rocks(w http.ResponseWriter, r *http.Request) {
	respond(w, http.StatusOK, map[string]any{"items": geology.Rocks()})
}
