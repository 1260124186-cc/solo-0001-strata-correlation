package correlation

import (
	"fmt"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/geology"
	"reflect"
	"sort"
	"strings"
	"time"
)

type InterpretationState string

const (
	DraftInterpretation InterpretationState = "draft"
	FinalInterpretation InterpretationState = "final"
)

const (
	MaxInterpretations        = 10000
	MaxInterpretationVersions = 500
	MaxInterpretationPairs    = 500
)

type Confidence string

const (
	LowConfidence    Confidence = "low"
	MediumConfidence Confidence = "medium"
	HighConfidence   Confidence = "high"
)

func ValidConfidence(c Confidence) bool {
	switch c {
	case LowConfidence, MediumConfidence, HighConfidence:
		return true
	}
	return false
}

// IntervalPair links a depth interval in the left referenced version with one
// in the right. Pairs within one interpretation are independent: they are not
// required to share a single depth offset.
type IntervalPair struct {
	LeftTopMM     int64 `json:"left_top_mm"`
	LeftBottomMM  int64 `json:"left_bottom_mm"`
	RightTopMM    int64 `json:"right_top_mm"`
	RightBottomMM int64 `json:"right_bottom_mm"`
}

type Interpretation struct {
	ID         string              `json:"id"`
	Left       Reference           `json:"left"`
	Right      Reference           `json:"right"`
	Pairs      []IntervalPair      `json:"pairs"`
	Rationale  string              `json:"rationale"`
	Confidence Confidence          `json:"confidence"`
	State      InterpretationState `json:"state"`
	Version    int                 `json:"version"`
	CreatedAt  time.Time           `json:"created_at"`
	UpdatedAt  time.Time           `json:"updated_at"`
}

type InterpretationRevision struct {
	Interpretation Interpretation `json:"interpretation"`
	Event          geology.Event  `json:"event"`
}

func (r InterpretationRevision) Clone() InterpretationRevision {
	r.Interpretation = r.Interpretation.Clone()
	return r
}

func (i Interpretation) Clone() Interpretation {
	i.Pairs = append([]IntervalPair{}, i.Pairs...)
	return i
}

func ValidateReferences(left, right Reference) error {
	if !geology.ValidID(left.ID, "prf_") || !geology.ValidID(right.ID, "prf_") {
		return geology.Invalid("reference", "剖面编号无效")
	}
	if left.Version < 1 || right.Version < 1 {
		return geology.Invalid("version", "需要指定正整数历史版本")
	}
	if left == right {
		return geology.Invalid("reference", "不能解释同一版本与自身的对应")
	}
	return nil
}

// NormalizeContent sorts pairs into a canonical order and trims the rationale.
// It checks structure only; interval depths are verified against the referenced
// versions separately in ValidatePairDepths.
func NormalizeContent(pairs []IntervalPair, rationale string, confidence Confidence) ([]IntervalPair, string, error) {
	if pairs == nil {
		return nil, "", geology.Invalid("pairs", "必须提供对应区间数组")
	}
	if len(pairs) == 0 {
		return nil, "", geology.Invalid("pairs", "至少记录一个对应区间")
	}
	if len(pairs) > MaxInterpretationPairs {
		return nil, "", geology.Invalid("pairs", "最多允许 500 个对应区间")
	}
	normalized := append([]IntervalPair{}, pairs...)
	sort.Slice(normalized, func(i, j int) bool {
		if normalized[i].LeftTopMM != normalized[j].LeftTopMM {
			return normalized[i].LeftTopMM < normalized[j].LeftTopMM
		}
		if normalized[i].LeftBottomMM != normalized[j].LeftBottomMM {
			return normalized[i].LeftBottomMM < normalized[j].LeftBottomMM
		}
		if normalized[i].RightTopMM != normalized[j].RightTopMM {
			return normalized[i].RightTopMM < normalized[j].RightTopMM
		}
		return normalized[i].RightBottomMM < normalized[j].RightBottomMM
	})
	for k, pair := range normalized {
		if pair.LeftTopMM < 0 || pair.LeftBottomMM <= pair.LeftTopMM || pair.RightTopMM < 0 || pair.RightBottomMM <= pair.RightTopMM {
			return nil, "", geology.Invalid(fmt.Sprintf("pairs[%d]", k), "对应区间必须为正厚度")
		}
	}
	rationale = strings.TrimSpace(rationale)
	if err := geology.Text("rationale", rationale, 1, 2000); err != nil {
		return nil, "", err
	}
	if !ValidConfidence(confidence) {
		return nil, "", geology.Invalid("confidence", "可信程度必须为 low、medium 或 high")
	}
	return normalized, rationale, nil
}

// ValidatePairDepths checks that every interval lies inside the depth range of
// its own referenced version.
func ValidatePairDepths(pairs []IntervalPair, leftDepth, rightDepth int64) error {
	for k, pair := range pairs {
		field := fmt.Sprintf("pairs[%d]", k)
		if pair.LeftTopMM < 0 || pair.LeftBottomMM <= pair.LeftTopMM || pair.LeftBottomMM > leftDepth {
			return geology.Invalid(field, "左侧区间必须为正厚度且位于引用版本深度内")
		}
		if pair.RightTopMM < 0 || pair.RightBottomMM <= pair.RightTopMM || pair.RightBottomMM > rightDepth {
			return geology.Invalid(field, "右侧区间必须为正厚度且位于引用版本深度内")
		}
	}
	return nil
}

func (i Interpretation) Validate() error {
	if !geology.ValidID(i.ID, "int_") {
		return geology.Invalid("id", "对应解释编号无效")
	}
	if err := ValidateReferences(i.Left, i.Right); err != nil {
		return err
	}
	pairs, rationale, err := NormalizeContent(i.Pairs, i.Rationale, i.Confidence)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(pairs, i.Pairs) || rationale != i.Rationale {
		return geology.Invalid("content", "对应解释内容未规范化")
	}
	if i.Version < 1 {
		return geology.Invalid("version", "版本必须为正整数")
	}
	if i.State != DraftInterpretation && i.State != FinalInterpretation {
		return geology.Invalid("state", "未知状态")
	}
	if i.CreatedAt.IsZero() || i.UpdatedAt.Before(i.CreatedAt) {
		return geology.Invalid("time", "时间顺序无效")
	}
	return nil
}
