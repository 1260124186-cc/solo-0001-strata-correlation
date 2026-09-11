package api

import "net/http"

func (h *Handler) diagnostics(w http.ResponseWriter, r *http.Request) {
	report, err := h.service.Diagnostics(r.Context())
	if err != nil {
		h.error(w, r, err)
		return
	}
	respond(w, http.StatusOK, report)
}
