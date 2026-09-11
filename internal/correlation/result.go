package correlation

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/geology"
	"sort"
	"strings"
	"time"
)

const Algorithm = "interval-v1"

type Reference struct {
	ID      string `json:"id"`
	Version int    `json:"version"`
}

type Request struct {
	Left           Reference `json:"left"`
	Right          Reference `json:"right"`
	OffsetMM       int64     `json:"offset_mm"`
	ExcludeMarkers []string  `json:"exclude_markers,omitempty"`
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
	ID              string       `json:"id"`
	Algorithm       string       `json:"algorithm"`
	Request         Request      `json:"request"`
	Segments        []Segment    `json:"segments"`
	Markers         []MarkerPair `json:"markers"`
	ExcludedMarkers []MarkerPair `json:"excluded_markers"`
	MissingMarkers  []string     `json:"missing_markers"`
	OverlapMM       int64        `json:"overlap_mm"`
	KnownMM         int64        `json:"known_mm"`
	EqualMM         int64        `json:"equal_mm"`
	Similarity      *float64     `json:"similarity"`
	CreatedAt       time.Time    `json:"created_at"`
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
	if len(r.ExcludeMarkers) > geology.MaxLayers {
		return geology.Invalid("exclude_markers", "剔除标志层最多 500 个")
	}
	seen := make(map[string]bool, len(r.ExcludeMarkers))
	for _, name := range r.ExcludeMarkers {
		if strings.TrimSpace(name) == "" {
			return geology.Invalid("exclude_markers", "剔除标志层名称不能为空")
		}
		if err := geology.Text("exclude_markers", name, 1, 80); err != nil {
			return err
		}
		key := strings.ToLower(strings.TrimSpace(name))
		if seen[key] {
			return geology.Invalid("exclude_markers", "剔除名单中的标志层名称不能重复")
		}
		seen[key] = true
	}
	return nil
}

// Canonicalized 返回剔除名单规范化后的请求副本：名称去空白、转小写、按名称排序。
// 名单顺序或大小写不影响身份，规范化后同样的输入必然得到同一编号。
func (r Request) Canonicalized() Request {
	if len(r.ExcludeMarkers) == 0 {
		r.ExcludeMarkers = []string{}
		return r
	}
	normalized := make([]string, 0, len(r.ExcludeMarkers))
	for _, name := range r.ExcludeMarkers {
		normalized = append(normalized, strings.ToLower(strings.TrimSpace(name)))
	}
	sort.Strings(normalized)
	r.ExcludeMarkers = normalized
	return r
}

func (r Request) Key() string {
	r = r.Canonicalized()
	b, _ := json.Marshal(struct {
		Algorithm string
		Request   Request
	}{Algorithm, r})
	sum := sha256.Sum256(b)
	return "cmp_" + hex.EncodeToString(sum[:16])
}

func (r Result) Clone() Result {
	r.Segments = append([]Segment{}, r.Segments...)
	r.Markers = append([]MarkerPair{}, r.Markers...)
	r.ExcludedMarkers = append([]MarkerPair{}, r.ExcludedMarkers...)
	r.MissingMarkers = append([]string{}, r.MissingMarkers...)
	if r.Similarity != nil {
		n := *r.Similarity
		r.Similarity = &n
	}
	return r
}
