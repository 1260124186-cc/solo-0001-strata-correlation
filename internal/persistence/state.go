package persistence

import (
	"encoding/json"
	"fmt"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/correlation"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/geology"
	"reflect"
)

type State struct {
	Schema          int                                             `json:"schema"`
	Histories       map[string][]geology.Revision                   `json:"histories"`
	Comparisons     map[string]correlation.Result                   `json:"comparisons"`
	Interpretations map[string][]correlation.InterpretationRevision `json:"interpretations"`
}

func emptyState() State {
	return State{
		Schema:          1,
		Histories:       map[string][]geology.Revision{},
		Comparisons:     map[string]correlation.Result{},
		Interpretations: map[string][]correlation.InterpretationRevision{},
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
	for id, revisions := range s.Interpretations {
		copies := make([]correlation.InterpretationRevision, len(revisions))
		for i, r := range revisions {
			copies[i] = r.Clone()
		}
		out.Interpretations[id] = copies
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
	if s.Schema != 1 || s.Histories == nil || s.Comparisons == nil || s.Interpretations == nil {
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
	for id, history := range s.Interpretations {
		if len(history) == 0 {
			return fmt.Errorf("empty interpretation history %s", id)
		}
		for i, r := range history {
			current := r.Interpretation
			if current.ID != id || current.Version != i+1 || r.Event.Version != i+1 || !r.Event.At.Equal(current.UpdatedAt) {
				return fmt.Errorf("inconsistent interpretation %s/%d", id, i+1)
			}
			if err := current.Validate(); err != nil {
				return fmt.Errorf("invalid interpretation %s: %w", id, err)
			}
			if err := geology.Text("reason", r.Event.Reason, 1, 500); err != nil {
				return err
			}
			left, err := s.Revision(current.Left.ID, current.Left.Version)
			if err != nil {
				return err
			}
			right, err := s.Revision(current.Right.ID, current.Right.Version)
			if err != nil {
				return err
			}
			if left.Profile.State != geology.Sealed || right.Profile.State != geology.Sealed {
				return fmt.Errorf("interpretation %s references unlocked version", id)
			}
			if err := correlation.ValidatePairDepths(current.Pairs, left.Profile.DepthMM, right.Profile.DepthMM); err != nil {
				return fmt.Errorf("invalid interpretation %s: %w", id, err)
			}
			if i == 0 {
				if r.Event.Action != "create" || current.State != correlation.DraftInterpretation {
					return fmt.Errorf("invalid initial interpretation")
				}
				continue
			}
			before := history[i-1].Interpretation
			if !before.CreatedAt.Equal(current.CreatedAt) || current.UpdatedAt.Before(before.UpdatedAt) {
				return fmt.Errorf("invalid interpretation chronology")
			}
			if current.Left != before.Left || current.Right != before.Right {
				return fmt.Errorf("interpretation references changed")
			}
			switch r.Event.Action {
			case "edit":
				if before.State != correlation.DraftInterpretation || current.State != correlation.DraftInterpretation {
					return fmt.Errorf("edited finalized interpretation")
				}
			case "revise":
				if before.State != correlation.FinalInterpretation || current.State != correlation.DraftInterpretation {
					return fmt.Errorf("invalid interpretation revision")
				}
			case "finalize":
				if before.State != correlation.DraftInterpretation || current.State != correlation.FinalInterpretation {
					return fmt.Errorf("invalid interpretation finalize")
				}
				if !interpretationContentEqual(before, current) {
					return fmt.Errorf("finalize changed interpretation content")
				}
			default:
				return fmt.Errorf("unknown interpretation action")
			}
		}
	}
	return nil
}

func interpretationContentEqual(a, b correlation.Interpretation) bool {
	return a.Rationale == b.Rationale && a.Confidence == b.Confidence && reflect.DeepEqual(a.Pairs, b.Pairs)
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
