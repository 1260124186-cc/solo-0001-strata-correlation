package api

import (
	"net/http"
)

type backupCreateInput struct {
	Note string `json:"note"`
}

type backupRestoreInput struct {
	ConfirmID string `json:"confirm_id"`
}

func (h *Handler) createBackup(w http.ResponseWriter, r *http.Request) {
	var input backupCreateInput
	if err := decode(w, r, &input); err != nil {
		h.error(w, r, err)
		return
	}
	meta, err := h.service.CreateBackup(r.Context(), input.Note)
	if err != nil {
		h.error(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/v1/backups/"+meta.ID)
	respond(w, http.StatusCreated, meta)
}

func (h *Handler) listBackups(w http.ResponseWriter, r *http.Request) {
	q, err := query(r.URL.RawQuery, "offset", "limit")
	if err != nil {
		h.error(w, r, err)
		return
	}
	offset, limit, err := pagination(q)
	if err != nil {
		h.error(w, r, err)
		return
	}
	page, err := h.service.ListBackups(r.Context(), offset, limit)
	if err != nil {
		h.error(w, r, err)
		return
	}
	respond(w, http.StatusOK, page)
}

func (h *Handler) inspectBackup(w http.ResponseWriter, r *http.Request) {
	detail, err := h.service.InspectBackup(r.Context(), r.PathValue("id"))
	if err != nil {
		h.error(w, r, err)
		return
	}
	respond(w, http.StatusOK, detail)
}

func (h *Handler) previewBackup(w http.ResponseWriter, r *http.Request) {
	preview, err := h.service.PreviewBackup(r.Context(), r.PathValue("id"))
	if err != nil {
		h.error(w, r, err)
		return
	}
	respond(w, http.StatusOK, preview)
}

func (h *Handler) restoreBackup(w http.ResponseWriter, r *http.Request) {
	var input backupRestoreInput
	if err := decode(w, r, &input); err != nil {
		h.error(w, r, err)
		return
	}
	result, err := h.service.RestoreBackup(r.Context(), r.PathValue("id"), input.ConfirmID)
	if err != nil {
		h.error(w, r, err)
		return
	}
	respond(w, http.StatusOK, result)
}
