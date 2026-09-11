package correlation

import (
	"fmt"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/geology"
	"strings"
)

// SegmentChange 描述同一深度区间在两个算法版本下的归类差异。
type SegmentChange struct {
	TopMM        int64             `json:"top_mm"`
	BottomMM     int64             `json:"bottom_mm"`
	ThicknessMM  int64             `json:"thickness_mm"`
	LeftRock     geology.Lithology `json:"left_rock"`
	RightRock    geology.Lithology `json:"right_rock"`
	FromRelation string            `json:"from_relation"`
	ToRelation   string            `json:"to_relation"`
}

type Metrics struct {
	OverlapMM  int64    `json:"overlap_mm"`
	KnownMM    int64    `json:"known_mm"`
	EqualMM    int64    `json:"equal_mm"`
	Similarity *float64 `json:"similarity"`
}

// Diff 是两个算法版本对同一组输入的结果差异，用于重算前说明。
type Diff struct {
	Request       Request         `json:"request"`
	FromAlgorithm string          `json:"from_algorithm"`
	ToAlgorithm   string          `json:"to_algorithm"`
	Identical     bool            `json:"identical"`
	FromMetrics   Metrics         `json:"from_metrics"`
	ToMetrics     Metrics         `json:"to_metrics"`
	Changes       []SegmentChange `json:"changes"`
	Summary       []string        `json:"summary"`
}

func metricsOf(r Result) Metrics {
	return Metrics{OverlapMM: r.OverlapMM, KnownMM: r.KnownMM, EqualMM: r.EqualMM, Similarity: r.Similarity}
}

func ratioText(v *float64) string {
	if v == nil {
		return "null（共同区间全部未知）"
	}
	return fmt.Sprintf("%.4f", *v)
}

// DiffOf 比较两份已算出的结果。它们应来自同一请求、不同算法版本。
func DiffOf(from, to Result) Diff {
	diff := Diff{
		Request:       to.Request,
		FromAlgorithm: from.Algorithm,
		ToAlgorithm:   to.Algorithm,
		FromMetrics:   metricsOf(from),
		ToMetrics:     metricsOf(to),
		Changes:       []SegmentChange{},
		Summary:       []string{},
	}
	index := make(map[[2]int64]Segment, len(from.Segments))
	for _, seg := range from.Segments {
		index[[2]int64{seg.TopMM, seg.BottomMM}] = seg
	}
	for _, seg := range to.Segments {
		old, exists := index[[2]int64{seg.TopMM, seg.BottomMM}]
		if exists && old.Relation != seg.Relation {
			diff.Changes = append(diff.Changes, SegmentChange{
				TopMM: seg.TopMM, BottomMM: seg.BottomMM, ThicknessMM: seg.BottomMM - seg.TopMM,
				LeftRock: seg.LeftRock, RightRock: seg.RightRock,
				FromRelation: old.Relation, ToRelation: seg.Relation,
			})
		}
	}
	diff.Identical = len(diff.Changes) == 0 &&
		from.OverlapMM == to.OverlapMM && from.KnownMM == to.KnownMM &&
		from.EqualMM == to.EqualMM && pointerFloatEqual(from.Similarity, to.Similarity)
	if diff.Identical {
		diff.Summary = append(diff.Summary, fmt.Sprintf(
			"两个版本结论一致：共同区间 %d 毫米，岩性相同 %d 毫米，相似度 %s，重算不会产生数值差异。",
			to.OverlapMM, to.EqualMM, ratioText(to.Similarity)))
		return diff
	}
	diff.Summary = append(diff.Summary, fmt.Sprintf("共同区间保持 %d 毫米不变（切分规则相同）。", to.OverlapMM))
	if from.KnownMM != to.KnownMM {
		diff.Summary = append(diff.Summary, fmt.Sprintf(
			"计入相似度的已知长度由 %d 毫米变为 %d 毫米（%s 把仅一侧未知的区间判为 different）。",
			from.KnownMM, to.KnownMM, to.Algorithm))
	}
	if from.EqualMM != to.EqualMM {
		diff.Summary = append(diff.Summary, fmt.Sprintf("岩性相同长度由 %d 毫米变为 %d 毫米。", from.EqualMM, to.EqualMM))
	}
	if !pointerFloatEqual(from.Similarity, to.Similarity) {
		diff.Summary = append(diff.Summary, fmt.Sprintf("相似度由 %s 变为 %s。", ratioText(from.Similarity), ratioText(to.Similarity)))
	}
	if len(diff.Changes) > 0 {
		diff.Summary = append(diff.Summary, fmt.Sprintf("共有 %d 个区间的关系归类发生变化（unknown→different），详见 changes。", len(diff.Changes)))
	}
	return diff
}

func pointerFloatEqual(a, b *float64) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// DescribeChanges 返回面向编录人员的区间差异文字，供重算确认前展示。
func (d Diff) DescribeChanges() string {
	var b strings.Builder
	for _, c := range d.Changes {
		fmt.Fprintf(&b, "%d-%d 毫米：%s 对 %s，%s → %s\n",
			c.TopMM, c.BottomMM, c.LeftRock, c.RightRock, c.FromRelation, c.ToRelation)
	}
	return strings.TrimRight(b.String(), "\n")
}
