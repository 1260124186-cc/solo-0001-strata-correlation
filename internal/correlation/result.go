package correlation

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/geology"
	"math"
	"time"
)

const Algorithm = "interval-v1"

// noSimilarityBits marks a null similarity; it is the bit pattern of a quiet
// NaN, which a non-negative ratio can never take.
var noSimilarityBits = math.Float64bits(math.NaN())

// ErrComparisonMismatch means a stored comparison cannot be reproduced from the
// locked revisions it references, so the saved content can no longer be trusted.
var ErrComparisonMismatch = errors.New("comparison content cannot be reproduced")

type Reference struct {
	ID      string `json:"id"`
	Version int    `json:"version"`
}

type Request struct {
	Left     Reference `json:"left"`
	Right    Reference `json:"right"`
	OffsetMM int64     `json:"offset_mm"`
}

type Segment struct {
	TopMM     int64             `json:"top_mm"`
	BottomMM  int64             `json:"bottom_mm"`
	LeftRock  geology.Lithology `json:"left_rock"`
	RightRock geology.Lithology `json:"right_rock"`
	Relation  string            `json:"relation"`
}

type MarkerPair struct {
	Name         string `json:"name"`
	LeftMM       int64  `json:"left_mm"`
	RightMM      int64  `json:"right_mm"`
	DifferenceMM int64  `json:"difference_mm"`
}

type Result struct {
	ID         string       `json:"id"`
	Algorithm  string       `json:"algorithm"`
	Request    Request      `json:"request"`
	Segments   []Segment    `json:"segments"`
	Markers    []MarkerPair `json:"markers"`
	OverlapMM  int64        `json:"overlap_mm"`
	KnownMM    int64        `json:"known_mm"`
	EqualMM    int64        `json:"equal_mm"`
	Similarity *float64     `json:"similarity"`
	CreatedAt  time.Time    `json:"created_at"`
}

func (r Request) Validate() error {
	if !geology.ValidID(r.Left.ID, "prf_") || !geology.ValidID(r.Right.ID, "prf_") {
		return geology.Invalid("reference", "剖面编号无效")
	}
	if r.Left.Version < 1 || r.Right.Version < 1 {
		return geology.Invalid("version", "需要指定正整数历史版本")
	}
	if r.Left == r.Right {
		return geology.Invalid("reference", "不能对比同一版本自身")
	}
	if r.OffsetMM < -geology.MaxDepth || r.OffsetMM > geology.MaxDepth {
		return geology.Invalid("offset_mm", "偏移超出一千米范围")
	}
	return nil
}

func (r Request) Key() string {
	b, _ := json.Marshal(struct {
		Algorithm string
		Request   Request
	}{Algorithm, r})
	sum := sha256.Sum256(b)
	return "cmp_" + hex.EncodeToString(sum[:16])
}

// Summary is the persisted content summary of a comparison. The per-segment
// detail and marker pairs are recomputed on read; the digest pins the exact
// content originally returned so drift is detected instead of guessed.
type Summary struct {
	SegmentCount int   `json:"segment_count"`
	MarkerCount  int   `json:"marker_count"`
	OverlapMM    int64 `json:"overlap_mm"`
	KnownMM      int64 `json:"known_mm"`
	EqualMM      int64 `json:"equal_mm"`
	// SimilarityBits is math.Float64bits of the ratio; a NaN bit pattern
	// stands for a null similarity, which no valid ratio can take.
	SimilarityBits uint64 `json:"similarity_bits,omitempty"`
	Digest         string `json:"digest"`
}

// Record is the persisted form: request parameters, algorithm version and a
// content summary. Segments and markers are derived from the referenced locked
// revisions whenever the result is read.
type Record struct {
	ID        string    `json:"id"`
	Algorithm string    `json:"algorithm"`
	Request   Request   `json:"request"`
	CreatedAt time.Time `json:"created_at"`
	Summary   Summary   `json:"summary"`

	// legacy holds a result saved by earlier builds that stored the full
	// per-segment detail. It is never marshalled by current code; on first
	// rewrite the record is compacted automatically.
	legacy *Result
}

// ContentDigest hashes every field that ever leaves the service for a result,
// including the segment and marker order. JSON field order follows struct
// declaration order deterministically, making the digest stable across runs.
func ContentDigest(r Result) string {
	content := struct {
		ID         string
		Algorithm  string
		Request    Request
		Segments   []Segment
		Markers    []MarkerPair
		OverlapMM  int64
		KnownMM    int64
		EqualMM    int64
		Similarity *float64
		CreatedAt  time.Time
	}{r.ID, r.Algorithm, r.Request, r.Segments, r.Markers, r.OverlapMM, r.KnownMM, r.EqualMM, r.Similarity, r.CreatedAt}
	b, _ := json.Marshal(content)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func summarize(r Result) Summary {
	s := Summary{
		SegmentCount:   len(r.Segments),
		MarkerCount:    len(r.Markers),
		OverlapMM:      r.OverlapMM,
		KnownMM:        r.KnownMM,
		EqualMM:        r.EqualMM,
		SimilarityBits: noSimilarityBits,
		Digest:         ContentDigest(r),
	}
	if r.Similarity != nil {
		s.SimilarityBits = math.Float64bits(*r.Similarity)
	}
	return s
}

// NewRecord builds the compact persisted record from a freshly computed result.
func NewRecord(r Result) Record {
	return Record{ID: r.ID, Algorithm: r.Algorithm, Request: r.Request, CreatedAt: r.CreatedAt, Summary: summarize(r)}
}

// Legacy wraps a result saved by earlier builds that still carries segments.
func Legacy(r Result) Record {
	return Record{ID: r.ID, Algorithm: r.Algorithm, Request: r.Request, CreatedAt: r.CreatedAt, Summary: summarize(r), legacy: &r}
}

// IsLegacy reports whether the record was loaded from an old-format snapshot
// that embeds the full per-segment detail.
func (d Record) IsLegacy() bool { return d.legacy != nil }

func (d Record) Clone() Record {
	out := d
	if d.legacy != nil {
		legacy := d.legacy.Clone()
		out.legacy = &legacy
	}
	return out
}

// Materialize reproduces the full result that was originally handed out.
// Recomputation goes through the same algorithm used at creation time and the
// content digest must match; a legacy record with an embedded detail is kept
// readable even if the recomputation drifts.
func (d Record) Materialize(left, right geology.Profile) (Result, error) {
	computed, err := Align(left, right, d.Request, d.CreatedAt)
	if err != nil {
		if d.legacy != nil {
			return d.legacy.Clone(), nil
		}
		return Result{}, err
	}
	if summarize(computed) != d.Summary {
		if d.legacy != nil {
			return d.legacy.Clone(), nil
		}
		return Result{}, fmt.Errorf("%w: %s", ErrComparisonMismatch, d.ID)
	}
	return computed, nil
}

// UnmarshalJSON accepts both the compact record and earlier snapshots that
// stored the complete result including segments and markers.
func (d *Record) UnmarshalJSON(b []byte) error {
	var probe struct {
		Segments *json.RawMessage `json:"segments"`
	}
	if err := json.Unmarshal(b, &probe); err != nil {
		return err
	}
	if probe.Segments != nil {
		var full Result
		if err := json.Unmarshal(b, &full); err != nil {
			return err
		}
		*d = Legacy(full)
		return nil
	}
	var compact struct {
		ID        string    `json:"id"`
		Algorithm string    `json:"algorithm"`
		Request   Request   `json:"request"`
		CreatedAt time.Time `json:"created_at"`
		Summary   Summary   `json:"summary"`
	}
	if err := json.Unmarshal(b, &compact); err != nil {
		return err
	}
	d.ID = compact.ID
	d.Algorithm = compact.Algorithm
	d.Request = compact.Request
	d.CreatedAt = compact.CreatedAt
	d.Summary = compact.Summary
	d.legacy = nil
	return nil
}

func (r Result) Clone() Result {
	r.Segments = append([]Segment{}, r.Segments...)
	r.Markers = append([]MarkerPair{}, r.Markers...)
	if r.Similarity != nil {
		n := *r.Similarity
		r.Similarity = &n
	}
	return r
}
