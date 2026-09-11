package api

import (
	"errors"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/catalog"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/geology"
	"net/http"
)

// reviewError renders the optimistic-concurrency 409 with the latest ordering
// attached, and falls back to the standard problem response otherwise.
func (h *Handler) reviewError(w http.ResponseWriter, r *http.Request, err error) {
	var sequence *catalog.SequenceConflict
	if errors.As(err, &sequence) {
		respond(w, http.StatusConflict, map[string]any{
			"error": geology.Problem{
				Code:   "conflict",
				Detail: sequence.Detail,
			},
			"expected_sequence": sequence.Expected,
			"actual_sequence":   sequence.Actual,
			"thread":            sequence.Thread,
		})
		return
	}
	h.error(w, r, err)
}

func (h *Handler) addReview(w http.ResponseWriter, r *http.Request) {
	var input catalog.ReviewInput
	if err := decode(w, r, &input); err != nil {
		h.error(w, r, err)
		return
	}
	view, err := h.service.AddReview(r.Context(), r.PathValue("id"), input)
	if err != nil {
		h.reviewError(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/v1/reviews/"+view.ID)
	respond(w, http.StatusCreated, view)
}

func (h *Handler) reviews(w http.ResponseWriter, r *http.Request) {
	page, err := h.service.Reviews(r.Context(), r.PathValue("id"))
	if err != nil {
		h.error(w, r, err)
		return
	}
	if len(page.Items) == 0 {
		h.error(w, r, geology.Missing("该对比结果还没有复核记录"))
		return
	}
	respond(w, http.StatusOK, page)
}

func (h *Handler) review(w http.ResponseWriter, r *http.Request) {
	view, err := h.service.Review(r.Context(), r.PathValue("rid"))
	if err != nil {
		h.error(w, r, err)
		return
	}
	respond(w, http.StatusOK, view)
}

func (h *Handler) confirmReview(w http.ResponseWriter, r *http.Request) {
	var input catalog.ConfirmInput
	if err := decode(w, r, &input); err != nil {
		h.error(w, r, err)
		return
	}
	sequence, err := integer(r.PathValue("seq"), "sequence", 0, 1, 1000)
	if err != nil {
		h.error(w, r, err)
		return
	}
	view, err := h.service.ConfirmReview(r.Context(), r.PathValue("rid"), sequence, input)
	if err != nil {
		h.reviewError(w, r, err)
		return
	}
	respond(w, http.StatusOK, view)
}

func (h *Handler) migrateReview(w http.ResponseWriter, r *http.Request) {
	var input catalog.MigrateInput
	if err := decode(w, r, &input); err != nil {
		h.error(w, r, err)
		return
	}
	view, err := h.service.MigrateReview(r.Context(), r.PathValue("rid"), input)
	if err != nil {
		h.reviewError(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/v1/reviews/"+view.ID)
	respond(w, http.StatusCreated, view)
}

func (h *Handler) deleteComparison(w http.ResponseWriter, r *http.Request) {
	if err := h.service.DeleteComparison(r.Context(), r.PathValue("id")); err != nil {
		h.error(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
