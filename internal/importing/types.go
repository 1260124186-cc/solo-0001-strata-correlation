package importing

import (
	"crypto/sha256"
	"encoding/hex"
	"time"

	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/geology"
)

// Status is the lifecycle state of a batch import.
type Status string

const (
	StatusPreview   Status = "preview"
	StatusCompleted Status = "completed"
	StatusFailed    Status = "failed"
)

const idPrefix = "imp_"

// IDFor derives a stable import id from the raw CSV bytes. Submitting the same
// file always addresses the same import, so finished imports can never run twice.
func IDFor(source []byte) string {
	sum := sha256.Sum256(source)
	return idPrefix + hex.EncodeToString(sum[:16])
}

// RawFields keeps the original (untrimmed) CSV cells of one record so preview
// errors can always be traced back to what the caller sent.
type RawFields struct {
	Name        string   `json:"name"`
	Site        string   `json:"site"`
	DepthMM     string   `json:"depth_mm"`
	Note        string   `json:"note"`
	TopMM       string   `json:"top_mm"`
	BottomMM    string   `json:"bottom_mm"`
	Rock        string   `json:"rock"`
	Description string   `json:"description"`
	Marker      string   `json:"marker"`
	Extra       []string `json:"extra,omitempty"`
}

// RowError is a preview error anchored to the original CSV line number.
type RowError struct {
	Line   int       `json:"line"`
	Field  string    `json:"field,omitempty"`
	Detail string    `json:"detail"`
	Raw    RawFields `json:"raw"`
}

// LayerDraft is a parsed layer annotated with its source line.
type LayerDraft struct {
	geology.Layer
	Line int `json:"line"`
}

// GroupDraft is the preview of one profile grouped out of the CSV.
type GroupDraft struct {
	Line    int          `json:"line"`
	Name    string       `json:"name"`
	Site    string       `json:"site"`
	DepthMM int64        `json:"depth_mm"`
	Note    string       `json:"note"`
	Layers  []LayerDraft `json:"layers"`
}

// Import is a persisted import job. SourceCSV is the exact submitted bytes; the
// preview is re-derivable from them, so previews survive service restarts.
type Import struct {
	ID          string       `json:"id"`
	Status      Status       `json:"status"`
	SourceCSV   []byte       `json:"source_csv"`
	Groups      []GroupDraft `json:"groups"`
	Errors      []RowError   `json:"errors"`
	ProfileIDs  []string     `json:"profile_ids,omitempty"`
	Failure     string       `json:"failure,omitempty"`
	CreatedAt   time.Time    `json:"created_at"`
	UpdatedAt   time.Time    `json:"updated_at"`
	CommittedAt *time.Time   `json:"committed_at,omitempty"`
}

// ImportView is the external representation; the raw CSV never leaves storage.
type ImportView struct {
	ID          string       `json:"id"`
	Status      Status       `json:"status"`
	Groups      []GroupDraft `json:"groups"`
	Errors      []RowError   `json:"errors"`
	ProfileIDs  []string     `json:"profile_ids,omitempty"`
	Failure     string       `json:"failure,omitempty"`
	CreatedAt   time.Time    `json:"created_at"`
	UpdatedAt   time.Time    `json:"updated_at"`
	CommittedAt *time.Time   `json:"committed_at,omitempty"`
}

func (m Import) View() ImportView {
	return ImportView{
		ID:          m.ID,
		Status:      m.Status,
		Groups:      append([]GroupDraft{}, m.Groups...),
		Errors:      append([]RowError{}, m.Errors...),
		ProfileIDs:  append([]string{}, m.ProfileIDs...),
		Failure:     m.Failure,
		CreatedAt:   m.CreatedAt,
		UpdatedAt:   m.UpdatedAt,
		CommittedAt: m.CommittedAt,
	}
}

func (m Import) Clone() Import {
	out := m
	if m.SourceCSV != nil {
		out.SourceCSV = append([]byte{}, m.SourceCSV...)
	}
	out.Groups = make([]GroupDraft, len(m.Groups))
	for i, g := range m.Groups {
		g.Layers = append([]LayerDraft{}, g.Layers...)
		out.Groups[i] = g
	}
	out.Errors = append([]RowError{}, m.Errors...)
	out.ProfileIDs = append([]string{}, m.ProfileIDs...)
	if m.CommittedAt != nil {
		t := *m.CommittedAt
		out.CommittedAt = &t
	}
	return out
}

// Valid reports whether the whole batch passes every rule. Confirmation only
// creates profiles when this is true.
func (m Import) Valid() bool {
	return m.Status == StatusPreview && len(m.Groups) > 0 && len(m.Errors) == 0
}
