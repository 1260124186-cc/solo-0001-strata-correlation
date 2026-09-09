package correlation

import (
	"github.com/1260124186-cc/solo-0003-strata-correlation/internal/geology"
	"strings"
	"time"
)

func Align(left, right geology.Profile, request Request, now time.Time) (Result, error) {
	if err := request.Validate(); err != nil {
		return Result{}, err
	}
	if err := left.Validate(); err != nil {
		return Result{}, err
	}
	if err := right.Validate(); err != nil {
		return Result{}, err
	}
	if left.State != geology.Sealed || right.State != geology.Sealed {
		return Result{}, geology.Conflict("对比需要已经锁定的历史版本")
	}
	if request.Left != (Reference{left.ID, left.Version}) || request.Right != (Reference{right.ID, right.Version}) {
		return Result{}, geology.Invalid("reference", "历史版本与输入不一致")
	}
	result := Result{ID: request.Key(), Algorithm: Algorithm, Request: request, Segments: []Segment{}, Markers: []MarkerPair{}, CreatedAt: now}
	i, j := 0, 0
	for i < len(left.Layers) && j < len(right.Layers) {
		a, b := left.Layers[i], right.Layers[j]
		top := max(a.TopMM, b.TopMM+request.OffsetMM)
		bottom := min(a.BottomMM, b.BottomMM+request.OffsetMM)
		if top < bottom {
			relation := "different"
			thickness := bottom - top
			result.OverlapMM += thickness
			if a.Rock == geology.Unknown || b.Rock == geology.Unknown {
				relation = "unknown"
			} else {
				result.KnownMM += thickness
				if a.Rock == b.Rock {
					relation = "equal"
					result.EqualMM += thickness
				}
			}
			result.Segments = append(result.Segments, Segment{top, bottom, a.Rock, b.Rock, relation})
		}
		leftEnd, rightEnd := a.BottomMM, b.BottomMM+request.OffsetMM
		if leftEnd <= rightEnd {
			i++
		}
		if rightEnd <= leftEnd {
			j++
		}
	}
	if result.OverlapMM == 0 {
		return Result{}, geology.Conflict("指定偏移后没有共同深度区间")
	}
	if result.KnownMM > 0 {
		ratio := float64(result.EqualMM) / float64(result.KnownMM)
		result.Similarity = &ratio
	}
	markers := make(map[string]geology.Layer)
	for _, layer := range right.Layers {
		if layer.Marker != "" {
			markers[strings.ToLower(layer.Marker)] = layer
		}
	}
	for _, layer := range left.Layers {
		if layer.Marker == "" {
			continue
		}
		other, exists := markers[strings.ToLower(layer.Marker)]
		if exists {
			rightTop := other.TopMM + request.OffsetMM
			result.Markers = append(result.Markers, MarkerPair{layer.Marker, layer.TopMM, rightTop, rightTop - layer.TopMM})
		}
	}
	return result, nil
}
