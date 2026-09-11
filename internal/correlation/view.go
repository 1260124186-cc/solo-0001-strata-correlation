package correlation

// SideStatus describes how one referenced version relates to the current
// state of its profile at read time.
type SideStatus struct {
	ReferencedVersion   int  `json:"referenced_version"`
	CurrentVersion      int  `json:"current_version"`
	LatestSealedVersion int  `json:"latest_sealed_version"`
	Stale               bool `json:"stale"`
}

// Freshness is computed at read time and never stored. Stale means at least
// one referenced version is no longer the current version of its profile.
// Regeneratable means a newer sealed input exists for at least one side.
type Freshness struct {
	Stale         bool       `json:"stale"`
	Regeneratable bool       `json:"regeneratable"`
	Left          SideStatus `json:"left"`
	Right         SideStatus `json:"right"`
	SupersededBy  []string   `json:"superseded_by,omitempty"`
}

// View is the read model of a Result: the immutable stored result plus its
// freshness against the current profile versions.
type View struct {
	Result
	Freshness Freshness `json:"freshness"`
}

func (v View) Clone() View {
	v.Result = v.Result.Clone()
	if v.Freshness.SupersededBy != nil {
		v.Freshness.SupersededBy = append([]string{}, v.Freshness.SupersededBy...)
	}
	return v
}
