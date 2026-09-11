package correlation

import (
	"sort"
	"strings"
)

type VersionChange struct {
	From int `json:"from"`
	To   int `json:"to"`
}

type MetricChange struct {
	From int64 `json:"from"`
	To   int64 `json:"to"`
}

type SimilarityChange struct {
	From *float64 `json:"from"`
	To   *float64 `json:"to"`
}

type SegmentChange struct {
	TopMM    int64   `json:"top_mm"`
	BottomMM int64   `json:"bottom_mm"`
	Before   Segment `json:"before"`
	After    Segment `json:"after"`
}

type SegmentDiff struct {
	Added   []Segment       `json:"added"`
	Removed []Segment       `json:"removed"`
	Changed []SegmentChange `json:"changed"`
}

type MarkerChange struct {
	Name   string     `json:"name"`
	Before MarkerPair `json:"before"`
	After  MarkerPair `json:"after"`
}

type MarkerDiff struct {
	Added   []MarkerPair   `json:"added"`
	Removed []MarkerPair   `json:"removed"`
	Changed []MarkerChange `json:"changed"`
}

// ResultDiff compares a regenerated result with the result it supersedes.
// Segments match by exact depth interval, markers by case-insensitive name;
// boundary shifts appear as removals plus additions.
type ResultDiff struct {
	FromID       string           `json:"from_id"`
	ToID         string           `json:"to_id"`
	LeftVersion  VersionChange    `json:"left_version"`
	RightVersion VersionChange    `json:"right_version"`
	OffsetMM     int64            `json:"offset_mm"`
	OverlapMM    MetricChange     `json:"overlap_mm"`
	KnownMM      MetricChange     `json:"known_mm"`
	EqualMM      MetricChange     `json:"equal_mm"`
	Similarity   SimilarityChange `json:"similarity"`
	Segments     SegmentDiff      `json:"segments"`
	Markers      MarkerDiff       `json:"markers"`
}

func DiffResults(from, to Result) ResultDiff {
	diff := ResultDiff{
		FromID:       from.ID,
		ToID:         to.ID,
		LeftVersion:  VersionChange{from.Request.Left.Version, to.Request.Left.Version},
		RightVersion: VersionChange{from.Request.Right.Version, to.Request.Right.Version},
		OffsetMM:     to.Request.OffsetMM,
		OverlapMM:    MetricChange{from.OverlapMM, to.OverlapMM},
		KnownMM:      MetricChange{from.KnownMM, to.KnownMM},
		EqualMM:      MetricChange{from.EqualMM, to.EqualMM},
		Similarity:   SimilarityChange{from.Similarity, to.Similarity},
		Segments:     diffSegments(from.Segments, to.Segments),
		Markers:      diffMarkers(from.Markers, to.Markers),
	}
	return diff
}

func diffSegments(before, after []Segment) SegmentDiff {
	type span struct{ top, bottom int64 }
	oldSegments := make(map[span]Segment)
	newSegments := make(map[span]Segment)
	spans := make(map[span]bool)
	for _, segment := range before {
		key := span{segment.TopMM, segment.BottomMM}
		oldSegments[key] = segment
		spans[key] = true
	}
	for _, segment := range after {
		key := span{segment.TopMM, segment.BottomMM}
		newSegments[key] = segment
		spans[key] = true
	}
	diff := SegmentDiff{Added: []Segment{}, Removed: []Segment{}, Changed: []SegmentChange{}}
	for key := range spans {
		old, hadOld := oldSegments[key]
		next, hasNext := newSegments[key]
		switch {
		case hadOld && hasNext && old != next:
			diff.Changed = append(diff.Changed, SegmentChange{key.top, key.bottom, old, next})
		case hadOld && !hasNext:
			diff.Removed = append(diff.Removed, old)
		case !hadOld && hasNext:
			diff.Added = append(diff.Added, next)
		}
	}
	bySpan := func(a, b Segment) bool {
		if a.TopMM != b.TopMM {
			return a.TopMM < b.TopMM
		}
		return a.BottomMM < b.BottomMM
	}
	sort.Slice(diff.Added, func(i, j int) bool { return bySpan(diff.Added[i], diff.Added[j]) })
	sort.Slice(diff.Removed, func(i, j int) bool { return bySpan(diff.Removed[i], diff.Removed[j]) })
	sort.Slice(diff.Changed, func(i, j int) bool {
		if diff.Changed[i].TopMM != diff.Changed[j].TopMM {
			return diff.Changed[i].TopMM < diff.Changed[j].TopMM
		}
		return diff.Changed[i].BottomMM < diff.Changed[j].BottomMM
	})
	return diff
}

func diffMarkers(before, after []MarkerPair) MarkerDiff {
	oldMarkers := make(map[string]MarkerPair)
	newMarkers := make(map[string]MarkerPair)
	names := make(map[string]string)
	for _, marker := range before {
		key := strings.ToLower(marker.Name)
		oldMarkers[key] = marker
		names[key] = marker.Name
	}
	for _, marker := range after {
		key := strings.ToLower(marker.Name)
		newMarkers[key] = marker
		names[key] = marker.Name
	}
	diff := MarkerDiff{Added: []MarkerPair{}, Removed: []MarkerPair{}, Changed: []MarkerChange{}}
	for key, name := range names {
		old, hadOld := oldMarkers[key]
		next, hasNext := newMarkers[key]
		switch {
		case hadOld && hasNext && old != next:
			diff.Changed = append(diff.Changed, MarkerChange{name, old, next})
		case hadOld && !hasNext:
			diff.Removed = append(diff.Removed, old)
		case !hadOld && hasNext:
			diff.Added = append(diff.Added, next)
		}
	}
	byName := func(a, b MarkerPair) bool { return strings.ToLower(a.Name) < strings.ToLower(b.Name) }
	sort.Slice(diff.Added, func(i, j int) bool { return byName(diff.Added[i], diff.Added[j]) })
	sort.Slice(diff.Removed, func(i, j int) bool { return byName(diff.Removed[i], diff.Removed[j]) })
	sort.Slice(diff.Changed, func(i, j int) bool { return diff.Changed[i].Name < diff.Changed[j].Name })
	return diff
}
