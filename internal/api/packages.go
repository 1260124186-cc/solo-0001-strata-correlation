package api

import (
	"errors"
	"io"
	"mime"
	"net/http"

	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/exchange"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/geology"
)

const packageBodyLimit = 64 << 20

type packageRequest struct {
	Selections []exchange.Selection `json:"selections"`
}

func (h *Handler) createPackage(w http.ResponseWriter, r *http.Request) {
	var input packageRequest
	if err := decode(w, r, &input); err != nil {
		h.error(w, r, err)
		return
	}
	pkg, reused, err := h.service.CreatePackage(r.Context(), input.Selections)
	if err != nil {
		h.error(w, r, err)
		return
	}
	status := http.StatusCreated
	if reused {
		status = http.StatusOK
	}
	w.Header().Set("Location", "/api/v1/exchange-packages/"+pkg.PackageID)
	respond(w, status, packageView(pkg))
}

func (h *Handler) getPackage(w http.ResponseWriter, r *http.Request) {
	pkg, err := h.service.Package(r.Context(), r.PathValue("id"))
	if err != nil {
		h.error(w, r, err)
		return
	}
	respond(w, http.StatusOK, packageView(pkg))
}

func (h *Handler) downloadPackage(w http.ResponseWriter, r *http.Request) {
	pkg, err := h.service.Package(r.Context(), r.PathValue("id"))
	if err != nil {
		h.error(w, r, err)
		return
	}
	data, err := exchange.Encode(pkg)
	if err != nil {
		h.error(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+pkg.PackageID+`.json"`)
	w.Header().Set("ETag", `"`+pkg.ContentDigest+`"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func (h *Handler) inspectPackage(w http.ResponseWriter, r *http.Request) {
	inspection, err := h.service.InspectPackage(r.Context(), r.PathValue("id"))
	if err != nil {
		h.error(w, r, err)
		return
	}
	status := http.StatusOK
	if !inspection.Valid {
		status = http.StatusUnprocessableEntity
	}
	respond(w, status, inspection)
}

func (h *Handler) inspectUploadedPackage(w http.ResponseWriter, r *http.Request) {
	contentType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || contentType != "application/json" {
		h.error(w, r, &geology.Problem{Code: "media_type", Detail: "请求类型必须为 application/json"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, packageBodyLimit)
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			h.error(w, r, &geology.Problem{Code: "too_large", Detail: "请求体超过 64 MiB"})
		} else {
			h.error(w, r, geology.Invalid("body", "JSON 格式错误"))
		}
		return
	}
	pkg, err := exchange.Decode(raw)
	if err != nil {
		issues := []string{"交换包不是可解析的 JSON 文档"}
		if err.Error() != "" {
			issues = []string{err.Error()}
		}
		respond(w, http.StatusUnprocessableEntity, exchange.Inspection{Valid: false, Issues: issues, Members: []exchange.MemberStatus{}, MissingReferences: []exchange.MissingStatusItem{}})
		return
	}
	inspection := h.service.InspectPackageData(r.Context(), pkg)
	status := http.StatusOK
	if !inspection.Valid {
		status = http.StatusUnprocessableEntity
	}
	respond(w, status, inspection)
}

type packageSummaryView struct {
	Format            string                      `json:"format"`
	PackageID         string                      `json:"package_id"`
	Status            string                      `json:"status"`
	Complete          bool                        `json:"complete"`
	Summary           exchange.Summary            `json:"summary"`
	ContentDigest     string                      `json:"content_digest"`
	Selections        []exchange.Selection        `json:"selections"`
	MissingReferences []exchange.MissingReference `json:"missing_references"`
	Manifest          exchange.Manifest           `json:"manifest"`
}

func packageView(pkg exchange.Envelope) packageSummaryView {
	return packageSummaryView{
		Format: pkg.Format, PackageID: pkg.PackageID, Status: pkg.Status, Complete: pkg.Complete,
		Summary: pkg.Summary, ContentDigest: pkg.ContentDigest, Selections: pkg.Payload.Selections,
		MissingReferences: pkg.Payload.MissingReferences, Manifest: pkg.Manifest,
	}
}
