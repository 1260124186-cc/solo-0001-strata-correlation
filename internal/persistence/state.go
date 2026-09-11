package persistence

import (
	"encoding/json"
	"fmt"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/correlation"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/geology"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/review"
	"reflect"
)

type State struct {
	Schema      int                           `json:"schema"`
	Histories   map[string][]geology.Revision `json:"histories"`
	Comparisons map[string]correlation.Result `json:"comparisons"`
	Reviews     map[string]review.Thread      `json:"reviews"`
}

func emptyState() State {
	return State{
		Schema:      1,
		Histories:   map[string][]geology.Revision{},
		Comparisons: map[string]correlation.Result{},
		Reviews:     map[string]review.Thread{},
	}
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
	for id, thread := range s.Reviews {
		out.Reviews[id] = thread.Clone()
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

func (s State) Validate() error {
	if s.Schema != 1 || s.Histories == nil || s.Comparisons == nil || s.Reviews == nil {
		return fmt.Errorf("unsupported snapshot shape")
	}
	for id, history := range s.Histories {
		if len(history) == 0 {
			return fmt.Errorf("empty history %s", id)
		}
		for i, r := range history {
			if r.Profile.ID != id || r.Profile.Version != i+1 || r.Event.Version != i+1 || !r.Event.At.Equal(r.Profile.UpdatedAt) {
				return fmt.Errorf("inconsistent revision %s/%d", id, i+1)
			}
			if err := r.Profile.Validate(); err != nil {
				return fmt.Errorf("invalid revision %s: %w", id, err)
			}
			if err := geology.Text("reason", r.Event.Reason, 1, 500); err != nil {
				return err
			}
			if i == 0 {
				if r.Event.Action != "create" || r.Profile.State != geology.Draft {
					return fmt.Errorf("invalid initial revision")
				}
			} else {
				before := history[i-1].Profile
				if !before.CreatedAt.Equal(r.Profile.CreatedAt) || r.Profile.UpdatedAt.Before(before.UpdatedAt) {
					return fmt.Errorf("invalid revision chronology")
				}
				if err := validateStep(before, r); err != nil {
					return err
				}
			}
		}
	}
	for id, result := range s.Comparisons {
		if id != result.ID || id != result.Request.Key() || result.Algorithm != correlation.Algorithm || result.CreatedAt.IsZero() {
			return fmt.Errorf("invalid comparison identity")
		}
		a, err := s.Revision(result.Request.Left.ID, result.Request.Left.Version)
		if err != nil {
			return err
		}
		b, err := s.Revision(result.Request.Right.ID, result.Request.Right.Version)
		if err != nil {
			return err
		}
		computed, err := correlation.Align(a.Profile, b.Profile, result.Request, result.CreatedAt)
		if err != nil {
			return err
		}
		expected, _ := json.Marshal(computed)
		actual, _ := json.Marshal(result)
		if string(expected) != string(actual) {
			return fmt.Errorf("comparison data mismatch")
		}
	}
	active := map[string]string{}
	for id, thread := range s.Reviews {
		if id != thread.ID {
			return fmt.Errorf("invalid review identity")
		}
		if err := thread.Validate(); err != nil {
			return fmt.Errorf("invalid review %s: %w", id, err)
		}
		// A mismatch merely means the result was recomputed after the thread
		// was written; the stale thread stays readable and must not be attached
		// to the new instance. Only matching threads count as active.
		if result, exists := s.Comparisons[thread.ComparisonID]; exists && result.Fingerprint == thread.Fingerprint {
			if other, conflict := active[thread.ComparisonID]; conflict {
				return fmt.Errorf("multiple active review threads for %s: %s and %s", thread.ComparisonID, other, id)
			}
			active[thread.ComparisonID] = id
		}
	}
	for id, thread := range s.Reviews {
		if thread.Origin != nil {
			source, ok := s.Reviews[thread.Origin.SourceThreadID]
			if !ok {
				return fmt.Errorf("review %s references missing migration source", id)
			}
			if source.ComparisonID != thread.ComparisonID {
				return fmt.Errorf("review %s migrated across comparisons", id)
			}
			found := false
			for _, m := range source.Migrations {
				if m.TargetThreadID == id {
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("review migration chain incomplete for %s", id)
			}
		}
		for _, m := range thread.Migrations {
			target, ok := s.Reviews[m.TargetThreadID]
			if !ok {
				return fmt.Errorf("review %s points at missing migration target", id)
			}
			if target.Origin == nil || target.Origin.SourceThreadID != id {
				return fmt.Errorf("review migration chain incomplete from %s", id)
			}
		}
	}
	return nil
}

func validateStep(before geology.Profile, r geology.Revision) error {
	after := r.Profile
	switch r.Event.Action {
	case "metadata", "layers":
		if before.State != geology.Draft || after.State != geology.Draft {
			return fmt.Errorf("edited sealed revision")
		}
		if r.Event.Action == "layers" && before.Metadata != after.Metadata {
			return fmt.Errorf("layers edit changed metadata")
		}
		if r.Event.Action == "metadata" && !reflect.DeepEqual(before.Layers, after.Layers) {
			return fmt.Errorf("metadata edit changed layers")
		}
	case "seal", "reopen":
		expected := geology.Sealed
		if r.Event.Action == "reopen" {
			expected = geology.Draft
		}
		if after.State != expected || before.State == expected || before.Metadata != after.Metadata || !reflect.DeepEqual(before.Layers, after.Layers) {
			return fmt.Errorf("invalid state change")
		}
	default:
		return fmt.Errorf("unknown revision action")
	}
	return nil
}
