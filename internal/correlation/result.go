package correlation

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/geology"
	"time"
)

const Algorithm = "interval-v1"

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
	ID          string       `json:"id"`
	Algorithm   string       `json:"algorithm"`
	Fingerprint string       `json:"fingerprint"`
	Request     Request      `json:"request"`
	Segments    []Segment    `json:"segments"`
	Markers     []MarkerPair `json:"markers"`
	OverlapMM   int64        `json:"overlap_mm"`
	KnownMM     int64        `json:"known_mm"`
	EqualMM     int64        `json:"equal_mm"`
	Similarity  *float64     `json:"similarity"`
	CreatedAt   time.Time    `json:"created_at"`
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

// StableFingerprint identifies the exact saved result instance. The comparison
// ID is derived from the inputs alone, so deleting and recomputing the same
// inputs reuses the ID but produces a different instance: the fingerprint also
// covers the full computed payload and creation time. The fingerprint field
// itself is deliberately excluded from its own input.
func (r Result) StableFingerprint() string {
	b, _ := json.Marshal(struct {
		Version    int
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
	}{
		Version: 1, ID: r.ID, Algorithm: r.Algorithm, Request: r.Request,
		Segments: r.Segments, Markers: r.Markers, OverlapMM: r.OverlapMM,
		KnownMM: r.KnownMM, EqualMM: r.EqualMM, Similarity: r.Similarity,
		CreatedAt: r.CreatedAt,
	})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
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
