package correlation

// VersionStatus relates the version referenced by one side of a result to
// the current latest version of that profile.
type VersionStatus struct {
	ReferencedVersion int  `json:"referenced_version"`
	LatestVersion     int  `json:"latest_version"`
	Current           bool `json:"current"`
}

// Currency reports whether the versions a result references are still the
// current versions of their profiles. It is derived at read time; the stored
// result itself never changes.
type Currency struct {
	Stale bool          `json:"stale"`
	Left  VersionStatus `json:"left"`
	Right VersionStatus `json:"right"`
}

// CurrencyOf builds the read-time currency view for a request given the
// latest version of each referenced profile.
func (r Request) CurrencyOf(leftLatest, rightLatest int) Currency {
	c := Currency{
		Left:  VersionStatus{r.Left.Version, leftLatest, r.Left.Version == leftLatest},
		Right: VersionStatus{r.Right.Version, rightLatest, r.Right.Version == rightLatest},
	}
	c.Stale = !c.Left.Current || !c.Right.Current
	return c
}

// Report is the read view of a result: the immutable result plus the
// currency derived from the current profile versions.
type Report struct {
	Result
	Currency Currency `json:"currency"`
}

// MetricChange compares one integer measurement between two results.
type MetricChange struct {
	Before int64 `json:"before"`
	After  int64 `json:"after"`
	Delta  int64 `json:"delta"`
}

// SimilarityChange compares nullable similarity ratios. Delta is null unless
// both sides have a known ratio.
type SimilarityChange struct {
	Before *float64 `json:"before"`
	After  *float64 `json:"after"`
	Delta  *float64 `json:"delta"`
}

// Difference summarizes how a refreshed result differs from the origin it
// supersedes.
type Difference struct {
	From       string           `json:"from"`
	To         string           `json:"to"`
	OverlapMM  MetricChange     `json:"overlap_mm"`
	KnownMM    MetricChange     `json:"known_mm"`
	EqualMM    MetricChange     `json:"equal_mm"`
	Similarity SimilarityChange `json:"similarity"`
	Segments   MetricChange     `json:"segments"`
	Markers    MetricChange     `json:"markers"`
}

func metricChange(before, after int64) MetricChange {
	return MetricChange{Before: before, After: after, Delta: after - before}
}

func similarityChange(before, after *float64) SimilarityChange {
	change := SimilarityChange{Before: before, After: after}
	if before != nil && after != nil {
		delta := *after - *before
		change.Delta = &delta
	}
	return change
}

// Diff summarizes the difference between an origin result and its refresh.
func Diff(before, after Result) Difference {
	return Difference{
		From:       before.ID,
		To:         after.ID,
		OverlapMM:  metricChange(before.OverlapMM, after.OverlapMM),
		KnownMM:    metricChange(before.KnownMM, after.KnownMM),
		EqualMM:    metricChange(before.EqualMM, after.EqualMM),
		Similarity: similarityChange(before.Similarity, after.Similarity),
		Segments:   metricChange(int64(len(before.Segments)), int64(len(after.Segments))),
		Markers:    metricChange(int64(len(before.Markers)), int64(len(after.Markers))),
	}
}
