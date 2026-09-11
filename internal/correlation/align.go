package correlation

import (
	"time"

	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/geology"
)

// Align computes a comparison with the current service-wide marker
// equivalence (interval-v2). Both profiles are validated strictly.
func Align(left, right geology.Profile, request Request, now time.Time) (Result, error) {
	if err := left.Validate(); err != nil {
		return Result{}, err
	}
	if err := right.Validate(); err != nil {
		return Result{}, err
	}
	return AlignValidated(Algorithm, left, right, request, now)
}

// AlignValidated assumes the caller already validated the profiles. Snapshot
// replay uses it after validating historical revisions with their
// grandfathered marker allowances; the algorithm itself stays frozen for
// interval-v1.
func AlignValidated(algorithm string, left, right geology.Profile, request Request, now time.Time) (Result, error) {
	if algorithm != AlgorithmV1 && algorithm != Algorithm {
		return Result{}, geology.Invalid("algorithm", "不支持的对比算法版本")
	}
	return align(algorithm, left, right, request, now)
}

// AlignWithAlgorithm reproduces a stored result with the algorithm recorded
// in it after strict validation. interval-v1 stays frozen for historical
// results, so saved conclusions never change.
func AlignWithAlgorithm(algorithm string, left, right geology.Profile, request Request, now time.Time) (Result, error) {
	if err := left.Validate(); err != nil {
		return Result{}, err
	}
	if err := right.Validate(); err != nil {
		return Result{}, err
	}
	return AlignValidated(algorithm, left, right, request, now)
}

func align(algorithm string, left, right geology.Profile, request Request, now time.Time) (Result, error) {
	if err := request.Validate(); err != nil {
		return Result{}, err
	}
	if left.State != geology.Sealed || right.State != geology.Sealed {
		return Result{}, geology.Conflict("对比需要已经锁定的历史版本")
	}
	if request.Left != (Reference{left.ID, left.Version}) || request.Right != (Reference{right.ID, right.Version}) {
		return Result{}, geology.Invalid("reference", "历史版本与输入不一致")
	}
	result := Result{ID: Key(algorithm, request), Algorithm: algorithm, Request: request, Segments: []Segment{}, Markers: []MarkerPair{}, CreatedAt: now}
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
	keyOf := markerKeyFunc(algorithm)
	for _, pair := range pairMarkers(left.Layers, right.Layers, keyOf) {
		rightTop := pair.right.TopMM + request.OffsetMM
		marker := MarkerPair{Name: pair.left.Marker, LeftMM: pair.left.TopMM, RightMM: rightTop, DifferenceMM: rightTop - pair.left.TopMM}
		// v1 JSON must stay byte-identical: it never carried right_name.
		if algorithm != AlgorithmV1 && keyOf(pair.right.Marker) == keyOf(pair.left.Marker) && pair.right.Marker != pair.left.Marker {
			marker.RightName = pair.right.Marker
		}
		result.Markers = append(result.Markers, marker)
	}
	return result, nil
}
