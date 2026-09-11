package correlation

import (
	"sort"

	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/geology"
)

type OffsetRequest struct {
	Left  Reference `json:"left"`
	Right Reference `json:"right"`
}

type Evidence struct {
	// Marker is the primary display spelling, taken from the left profile.
	// LeftMarker and RightMarker expose both cataloguers' raw spellings so
	// case/width variants stay explainable in the suggestion output.
	Marker           string `json:"marker"`
	LeftMarker       string `json:"left_marker"`
	RightMarker      string `json:"right_marker"`
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

// Suggest uses the current service-wide marker equivalence (geology.MarkerKey):
// any name pair that would be rejected as a duplicate inside one profile is
// also matched here and vice versa. The pairing itself is the same code used
// for comparison evidence.
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
	result := OffsetProposal{Comparison: request, Evidence: []Evidence{}}
	offsets := make([]int64, 0)
	for _, pair := range pairMarkers(left.Layers, right.Layers, geology.MarkerKey) {
		difference := pair.left.TopMM - pair.right.TopMM
		offsets = append(offsets, difference)
		result.Evidence = append(result.Evidence, Evidence{
			Marker:           pair.left.Marker,
			LeftMarker:       pair.left.Marker,
			RightMarker:      pair.right.Marker,
			LeftTopMM:        pair.left.TopMM,
			RightTopMM:       pair.right.TopMM,
			RequiredOffsetMM: difference,
		})
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
	result.ExpectedOverlapMM = max(0, bottom-top)
	if result.ExpectedOverlapMM == 0 {
		return OffsetProposal{}, geology.Conflict("共同标志层无法形成有效共同区间")
	}
	return result, nil
}
