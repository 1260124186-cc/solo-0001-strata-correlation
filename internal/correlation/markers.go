package correlation

import (
	"sort"
	"strings"

	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/geology"
)

// markerKeyFunc selects the equivalence rule. AlgorithmV1 is the frozen
// historical rule (Unicode lower-case only); everything current uses the
// service-wide geology.MarkerKey, so duplicate rejection, offset matching
// and comparison evidence can never disagree.
func markerKeyFunc(algorithm string) func(string) string {
	if algorithm == AlgorithmV1 {
		return strings.ToLower
	}
	return geology.MarkerKey
}

type keyedLayer struct {
	key   string
	layer geology.Layer
}

// collectMarkers groups non-empty marker layers by their equivalence key in
// stable top-depth order.
func collectMarkers(layers []geology.Layer, keyOf func(string) string) map[string][]keyedLayer {
	groups := map[string][]keyedLayer{}
	for _, layer := range layers {
		if layer.Marker == "" {
			continue
		}
		key := keyOf(layer.Marker)
		groups[key] = append(groups[key], keyedLayer{key: key, layer: layer})
	}
	for key := range groups {
		sort.SliceStable(groups[key], func(i, j int) bool {
			return groups[key][i].layer.TopMM < groups[key][j].layer.TopMM
		})
	}
	return groups
}

// pairMarkers matches left markers against right markers one-to-one inside
// each equivalence key, in top-depth order. Only equivalence differs between
// algorithms; ordering and display rules are shared so every consumer shows
// the same names. The left raw spelling is the primary display name; the
// right spelling is carried separately when the two cataloguers differ.
func pairMarkers(left, right []geology.Layer, keyOf func(string) string) []matchedMarker {
	rightGroups := collectMarkers(right, keyOf)
	leftMarkers := make([]keyedLayer, 0)
	for _, layer := range left {
		if layer.Marker != "" {
			leftMarkers = append(leftMarkers, keyedLayer{key: keyOf(layer.Marker), layer: layer})
		}
	}
	sort.SliceStable(leftMarkers, func(i, j int) bool {
		return leftMarkers[i].layer.TopMM < leftMarkers[j].layer.TopMM
	})
	cursors := map[string]int{}
	matched := make([]matchedMarker, 0)
	for _, l := range leftMarkers {
		idx := cursors[l.key]
		pool := rightGroups[l.key]
		if idx >= len(pool) {
			continue
		}
		cursors[l.key] = idx + 1
		matched = append(matched, matchedMarker{left: l.layer, right: pool[idx].layer})
	}
	return matched
}

type matchedMarker struct {
	left  geology.Layer
	right geology.Layer
}
