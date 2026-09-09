package correlation

import (
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/geology"
	"sort"
	"strings"
)

type OffsetRequest struct {
	Left  Reference `json:"left"`
	Right Reference `json:"right"`
}

type Evidence struct {
	Marker           string `json:"marker"`
	LeftTopMM        int64  `json:"left_top_mm"`
	RightTopMM       int64  `json:"right_top_mm"`
	RequiredOffsetMM int64  `json:"required_offset_mm"`
	ResidualMM       int64  `json:"residual_mm"`
}

type OffsetProposal struct {
	Comparison             Request    `json:"comparison"`
	Evidence               []Evidence `json:"evidence"`
	MaxResidualMM          int64      `json:"max_residual_mm"`
	MeanAbsoluteResidualMM float64    `json:"mean_absolute_residual_mm"`
	ExpectedOverlapMM      int64      `json:"expected_overlap_mm"`
	Ambiguous              bool       `json:"ambiguous"`
}

// The median minimizes the total absolute marker discrepancy. An even number
// uses the midpoint of the two middle values, rounded toward zero in millimetres.
func Suggest(left, right geology.Profile, input OffsetRequest) (OffsetProposal, error) {
	request := Request{Left: input.Left, Right: input.Right}
	if err := request.Validate(); err != nil {
		return OffsetProposal{}, err
	}
	if left.ID != input.Left.ID || left.Version != input.Left.Version || right.ID != input.Right.ID || right.Version != input.Right.Version {
		return OffsetProposal{}, geology.Invalid("reference", "输入与版本不一致")
	}
	if left.State != geology.Sealed || right.State != geology.Sealed {
		return OffsetProposal{}, geology.Conflict("偏移建议需要两个锁定版本")
	}
	markers := make(map[string]geology.Layer)
	for _, layer := range right.Layers {
		if layer.Marker != "" {
			markers[strings.ToLower(layer.Marker)] = layer
		}
	}
	result := OffsetProposal{Comparison: request, Evidence: []Evidence{}}
	offsets := make([]int64, 0)
	for _, layer := range left.Layers {
		if layer.Marker == "" {
			continue
		}
		other, exists := markers[strings.ToLower(layer.Marker)]
		if !exists {
			continue
		}
		difference := layer.TopMM - other.TopMM
		offsets = append(offsets, difference)
		result.Evidence = append(result.Evidence, Evidence{Marker: layer.Marker, LeftTopMM: layer.TopMM, RightTopMM: other.TopMM, RequiredOffsetMM: difference})
	}
	if len(offsets) == 0 {
		return OffsetProposal{}, geology.Conflict("两个版本没有共同标志层")
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
		return OffsetProposal{}, geology.Conflict("共同标志层无法形成有效共同区间")
	}
	return result, nil
}
