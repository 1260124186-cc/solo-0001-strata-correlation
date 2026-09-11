package correlation

import (
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/geology"
	"sort"
	"strings"
)

type OffsetRequest struct {
	Left           Reference `json:"left"`
	Right          Reference `json:"right"`
	ExcludeMarkers []string  `json:"exclude_markers,omitempty"`
}

type Evidence struct {
	Marker           string `json:"marker"`
	LeftTopMM        int64  `json:"left_top_mm"`
	RightTopMM       int64  `json:"right_top_mm"`
	RequiredOffsetMM int64  `json:"required_offset_mm"`
	ResidualMM       int64  `json:"residual_mm"`
}

type OffsetProposal struct {
	Comparison Request `json:"comparison"`
	// Evidence 仅包含本次参与中位数计算的共同标志层。
	Evidence []Evidence `json:"evidence"`
	// ExcludedMarkers 是两侧共有但按请求显式剔除、不参与计算的标志层。
	ExcludedMarkers []Evidence `json:"excluded_markers"`
	// MissingMarkers 是剔除名单中找不到对应共同标志层的名称。
	MissingMarkers         []string `json:"missing_markers"`
	MaxResidualMM          int64    `json:"max_residual_mm"`
	MeanAbsoluteResidualMM float64  `json:"mean_absolute_residual_mm"`
	ExpectedOverlapMM      int64    `json:"expected_overlap_mm"`
	Ambiguous              bool     `json:"ambiguous"`
}

// commonMarker 记录一对同名标志层的名称与顶深，名称按忽略大小写匹配。
type commonMarker struct {
	name     string
	leftTop  int64
	rightTop int64
}

// classifyMarkers 按左侧分层顺序收集共同标志层，并将它们分为采用与剔除两类；
// 剔除名单中不存在共同标志层的名称归入 missing。
func classifyMarkers(left, right geology.Profile, excluded map[string]bool) (used, dropped []commonMarker, missing []string) {
	used = []commonMarker{}
	dropped = []commonMarker{}
	missing = []string{}
	rightMarkers := make(map[string]geology.Layer)
	for _, layer := range right.Layers {
		if layer.Marker != "" {
			rightMarkers[strings.ToLower(layer.Marker)] = layer
		}
	}
	found := make(map[string]bool)
	for _, layer := range left.Layers {
		if layer.Marker == "" {
			continue
		}
		key := strings.ToLower(layer.Marker)
		other, exists := rightMarkers[key]
		if !exists {
			continue
		}
		found[key] = true
		pair := commonMarker{name: layer.Marker, leftTop: layer.TopMM, rightTop: other.TopMM}
		if excluded[key] {
			dropped = append(dropped, pair)
		} else {
			used = append(used, pair)
		}
	}
	for name := range excluded {
		if !found[name] {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	return used, dropped, missing
}

func toEvidence(pair commonMarker, median int64, withResidual bool) Evidence {
	offset := pair.leftTop - pair.rightTop
	evidence := Evidence{
		Marker:           pair.name,
		LeftTopMM:        pair.leftTop,
		RightTopMM:       pair.rightTop,
		RequiredOffsetMM: offset,
	}
	if withResidual {
		evidence.ResidualMM = median - offset
	}
	return evidence
}

// The median minimizes the total absolute marker discrepancy. An even number
// uses the midpoint of the two middle values, rounded toward zero in millimetres.
func Suggest(left, right geology.Profile, input OffsetRequest) (OffsetProposal, error) {
	request := Request{Left: input.Left, Right: input.Right, ExcludeMarkers: input.ExcludeMarkers}
	if err := request.Validate(); err != nil {
		return OffsetProposal{}, err
	}
	request = request.Canonicalized()
	if left.ID != input.Left.ID || left.Version != input.Left.Version || right.ID != input.Right.ID || right.Version != input.Right.Version {
		return OffsetProposal{}, geology.Invalid("reference", "输入与版本不一致")
	}
	if left.State != geology.Sealed || right.State != geology.Sealed {
		return OffsetProposal{}, geology.Conflict("偏移建议需要两个锁定版本")
	}
	excluded := make(map[string]bool, len(request.ExcludeMarkers))
	for _, name := range request.ExcludeMarkers {
		excluded[name] = true
	}
	used, dropped, missing := classifyMarkers(left, right, excluded)
	if len(used)+len(dropped) == 0 {
		return OffsetProposal{}, geology.Conflict("两个版本没有共同标志层")
	}
	result := OffsetProposal{
		Comparison:      request,
		Evidence:        []Evidence{},
		ExcludedMarkers: []Evidence{},
		MissingMarkers:  missing,
	}
	for _, pair := range dropped {
		result.ExcludedMarkers = append(result.ExcludedMarkers, toEvidence(pair, 0, false))
	}
	if len(used) == 0 {
		return OffsetProposal{}, geology.Conflict("共同标志层已全部剔除，没有有效证据计算偏移")
	}
	offsets := make([]int64, 0, len(used))
	for _, pair := range used {
		offsets = append(offsets, pair.leftTop-pair.rightTop)
		result.Evidence = append(result.Evidence, toEvidence(pair, 0, false))
	}
	sort.Slice(offsets, func(i, j int) bool { return offsets[i] < offsets[j] })
	middle := len(offsets) / 2
	median := offsets[middle]
	if len(offsets)%2 == 0 {
		median = (offsets[middle-1] + offsets[middle]) / 2
	}
	result.Comparison.OffsetMM = median
	var total int64
	for i := range result.Evidence {
		residual := median - result.Evidence[i].RequiredOffsetMM
		result.Evidence[i].ResidualMM = residual
		absolute := residual
		if absolute < 0 {
			absolute = -absolute
		}
		total += absolute
		result.MaxResidualMM = max(result.MaxResidualMM, absolute)
	}
	result.MeanAbsoluteResidualMM = float64(total) / float64(len(offsets))
	result.Ambiguous = offsets[0] != offsets[len(offsets)-1]
	top := max(int64(0), median)
	bottom := min(left.DepthMM, right.DepthMM+median)
	result.ExpectedOverlapMM = max(int64(0), bottom-top)
	if result.ExpectedOverlapMM == 0 {
		return OffsetProposal{}, geology.Conflict("采用的标志层无法形成有效共同区间")
	}
	return result, nil
}
