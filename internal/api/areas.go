package api

import (
	"net/http"

	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/catalog"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/geology"
)

func (h *Handler) createArea(w http.ResponseWriter, r *http.Request) {
	var input catalog.CreateArea
	if err := decode(w, r, &input); err != nil {
		h.error(w, r, err)
		return
	}
	status, err := h.service.CreateArea(r.Context(), input)
	if err != nil {
		h.error(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/v1/areas/"+status.ID)
	respond(w, http.StatusCreated, status)
}

func (h *Handler) listAreas(w http.ResponseWriter, r *http.Request) {
	list, err := h.service.Areas(r.Context())
	if err != nil {
		h.error(w, r, err)
		return
	}
	respond(w, http.StatusOK, list)
}

func (h *Handler) getArea(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !geology.ValidAreaID(id) {
		h.error(w, r, geology.Invalid("id", "研究区编号无效"))
		return
	}
	status, global, err := h.service.Area(r.Context(), id)
	if err != nil {
		h.error(w, r, err)
		return
	}
	respond(w, http.StatusOK, struct {
		catalog.AreaStatus
		Global catalog.GlobalUsage `json:"global"`
	}{status, global})
}
