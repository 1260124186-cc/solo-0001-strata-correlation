package persistence

import (
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/correlation"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/geology"
)

type State struct {
	Schema      int                           `json:"schema"`
	Histories   map[string][]geology.Revision `json:"histories"`
	Comparisons map[string]correlation.Result `json:"comparisons"`
}

func emptyState() State {
	return State{Schema: 1, Histories: map[string][]geology.Revision{}, Comparisons: map[string]correlation.Result{}}
}

func (s State) Clone() State {
	out := emptyState()
	for id, revisions := range s.Histories {
		copies := make([]geology.Revision, len(revisions))
		for i, r := range revisions {
			copies[i] = r.Clone()
		}
		out.Histories[id] = copies
	}
	for id, result := range s.Comparisons {
		out.Comparisons[id] = result.Clone()
	}
	return out
}

func (s State) Latest(id string) (geology.Profile, error) {
	history, ok := s.Histories[id]
	if !ok || len(history) == 0 {
		return geology.Profile{}, geology.Missing("剖面不存在")
	}
	return history[len(history)-1].Profile.Clone(), nil
}

func (s State) Revision(id string, version int) (geology.Revision, error) {
	history, ok := s.Histories[id]
	if !ok {
		return geology.Revision{}, geology.Missing("剖面不存在")
	}
	if version < 1 || version > len(history) {
		return geology.Revision{}, geology.Missing("历史版本不存在")
	}
	return history[version-1].Clone(), nil
}
