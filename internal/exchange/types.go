package exchange

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/correlation"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/geology"
)

const (
	// Format is the wire format version. Future readers must migrate v1 and
	// reject versions they do not understand instead of trusting them.
	Format = "exchange-package/v1"

	KindProfileRevision = "profile_revision"
	KindProfileHistory  = "profile_history"
	KindComparison      = "comparison"

	StatusComplete   = "complete"
	StatusIncomplete = "incomplete"
)

type Selection struct {
	Type    string `json:"type"`
	ID      string `json:"id"`
	Version int    `json:"version,omitempty"`
}

type Reference struct {
	Kind    string `json:"kind"`
	ID      string `json:"id,omitempty"`
	Version int    `json:"version,omitempty"`
}

type MissingReference struct {
	Kind         string    `json:"kind"`
	ID           string    `json:"id"`
	Version      int       `json:"version,omitempty"`
	ReferencedBy Reference `json:"referenced_by"`
	Relation     string    `json:"relation"`
}

type ProfileHistory struct {
	ProfileID string             `json:"profile_id"`
	Revisions []geology.Revision `json:"revisions"`
}

type RevisionSummary struct {
	ProfileID  string        `json:"profile_id"`
	Version    int           `json:"version"`
	State      geology.State `json:"state"`
	Name       string        `json:"name"`
	Site       string        `json:"site"`
	DepthMM    int64         `json:"depth_mm"`
	LayerCount int           `json:"layer_count"`
	UpdatedAt  time.Time     `json:"updated_at"`
	Digest     string        `json:"content_digest"`
}

type HistorySummary struct {
	ProfileID           string            `json:"profile_id"`
	RevisionCount       int               `json:"revision_count"`
	SealedRevisionCount int               `json:"sealed_revision_count"`
	Versions            []int             `json:"versions"`
	Revisions           []RevisionSummary `json:"revisions"`
}

type ComparisonSummary struct {
	ID           string                `json:"id"`
	Algorithm    string                `json:"algorithm"`
	Left         correlation.Reference `json:"left"`
	Right        correlation.Reference `json:"right"`
	OffsetMM     int64                 `json:"offset_mm"`
	OverlapMM    int64                 `json:"overlap_mm"`
	KnownMM      int64                 `json:"known_mm"`
	EqualMM      int64                 `json:"equal_mm"`
	Similarity   *float64              `json:"similarity"`
	SegmentCount int                   `json:"segment_count"`
	MarkerCount  int                   `json:"marker_count"`
	CreatedAt    time.Time             `json:"created_at"`
	Digest       string                `json:"content_digest"`
}

type Member struct {
	Kind          string          `json:"kind"`
	ID            string          `json:"id"`
	ContentDigest string          `json:"content_digest"`
	Summary       json.RawMessage `json:"summary"`
}

type Manifest struct {
	Format            string             `json:"format"`
	Members           []Member           `json:"members"`
	MissingReferences []MissingReference `json:"missing_references"`
}

type Summary struct {
	HistoryCount          int    `json:"history_count"`
	RevisionCount         int    `json:"revision_count"`
	ComparisonCount       int    `json:"comparison_count"`
	MissingReferenceCount int    `json:"missing_reference_count"`
	ContentDigest         string `json:"content_digest"`
}

type Payload struct {
	Format            string               `json:"format"`
	Selections        []Selection          `json:"selections"`
	Histories         []ProfileHistory     `json:"histories"`
	Comparisons       []correlation.Result `json:"comparisons"`
	MissingReferences []MissingReference   `json:"missing_references"`
}

type Envelope struct {
	Format        string   `json:"format"`
	PackageID     string   `json:"package_id"`
	SelectionKey  string   `json:"selection_key"`
	Status        string   `json:"status"`
	Complete      bool     `json:"complete"`
	Summary       Summary  `json:"summary"`
	ContentDigest string   `json:"content_digest"`
	Payload       Payload  `json:"payload"`
	Manifest      Manifest `json:"manifest"`
}

type Dataset struct {
	Histories   map[string][]geology.Revision
	Comparisons map[string]correlation.Result
}

// canonical renders JSON using its data semantics, not the sender's object key
// order or insignificant whitespace. All package digests therefore survive a
// harmless reordering of the same JSON document.
func canonical(value any) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var normalized any
	if err := json.Unmarshal(raw, &normalized); err != nil {
		return nil, err
	}
	return json.Marshal(normalized)
}

func digestValue(value any) (string, error) {
	raw, err := canonical(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func packageIDFromDigest(digest string) string {
	if len(digest) != len("sha256:")+64 {
		return ""
	}
	return "pkg_" + digest[len("sha256:"):len("sha256:")+32]
}

func SelectionKey(selections []Selection) (string, error) {
	raw, err := canonical(selections)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return "sel_" + hex.EncodeToString(sum[:32]), nil
}

func missingKey(m MissingReference) string {
	return fmt.Sprintf("%s\x00%s\x00%d\x00%s\x00%s\x00%d\x00%s",
		m.Kind, m.ID, m.Version, m.ReferencedBy.Kind, m.ReferencedBy.ID, m.ReferencedBy.Version, m.Relation)
}

func sortedComparisonIDs(data Dataset) []string {
	ids := make([]string, 0, len(data.Comparisons))
	for id := range data.Comparisons {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func revisionExists(history []geology.Revision, version int) bool {
	return version >= 1 && version <= len(history)
}
