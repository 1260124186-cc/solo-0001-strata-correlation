package persistence

import (
	"encoding/json"
	"fmt"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/correlation"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/geology"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/importing"
	"reflect"
)

type State struct {
	Schema      int                           `json:"schema"`
	Histories   map[string][]geology.Revision `json:"histories"`
	Comparisons map[string]correlation.Result `json:"comparisons"`
	// Imports holds durable CSV import jobs and their previews. Absent in older
	// snapshots; a nil map is treated as empty so existing data still loads.
	Imports map[string]importing.Import `json:"imports,omitempty"`
}

func emptyState() State {
	return State{
		Schema:      1,
		Histories:   map[string][]geology.Revision{},
		Comparisons: map[string]correlation.Result{},
		Imports:     map[string]importing.Import{},
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
	for id, job := range s.Imports {
		out.Imports[id] = job.Clone()
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
	if s.Schema != 1 || s.Histories == nil || s.Comparisons == nil {
		return fmt.Errorf("unsupported snapshot shape")
	}
	if s.Imports == nil {
		s.Imports = map[string]importing.Import{}
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
	for id, job := range s.Imports {
		if err := validateImport(id, job, s); err != nil {
			return err
		}
	}
	return nil
}

func validateImport(id string, job importing.Import, s State) error {
	if id != job.ID || !geology.ValidID(job.ID, "imp_") {
		return fmt.Errorf("invalid import identity %s", id)
	}
	if importing.IDFor(job.SourceCSV) != job.ID {
		return fmt.Errorf("import source mismatch %s", id)
	}
	if job.CreatedAt.IsZero() || job.UpdatedAt.Before(job.CreatedAt) || len(job.SourceCSV) == 0 {
		return fmt.Errorf("invalid import record %s", id)
	}
	// The stored preview must be exactly what the stored raw CSV produces.
	reparsed, err := importing.Parse(job.SourceCSV, job.CreatedAt)
	if err != nil {
		return fmt.Errorf("import %s source unparseable: %w", id, err)
	}
	if !reflect.DeepEqual(reparsed.Groups, job.Groups) || !reflect.DeepEqual(reparsed.Errors, job.Errors) {
		return fmt.Errorf("import %s preview does not match source", id)
	}
	switch job.Status {
	case importing.StatusPreview:
		if job.CommittedAt != nil || len(job.ProfileIDs) != 0 || job.Failure != "" {
			return fmt.Errorf("invalid preview import %s", id)
		}
	case importing.StatusFailed:
		if job.CommittedAt != nil || len(job.ProfileIDs) != 0 || job.Failure == "" {
			return fmt.Errorf("invalid failed import %s", id)
		}
	case importing.StatusCompleted:
		if job.CommittedAt == nil || job.Failure != "" || len(job.ProfileIDs) != len(job.Groups) {
			return fmt.Errorf("invalid completed import %s", id)
		}
		if len(job.Errors) != 0 || len(job.Groups) == 0 {
			return fmt.Errorf("completed import %s has errors", id)
		}
		seen := make(map[string]bool, len(job.ProfileIDs))
		for i, profileID := range job.ProfileIDs {
			if seen[profileID] {
				return fmt.Errorf("import %s lists a profile twice", id)
			}
			seen[profileID] = true
			history, ok := s.Histories[profileID]
			if !ok || len(history) == 0 {
				return fmt.Errorf("import %s references missing profile", id)
			}
			profile := history[0].Profile
			group := job.Groups[i]
			if profile.Name != group.Name || profile.Site != group.Site || profile.DepthMM != group.DepthMM ||
				profile.Note != group.Note || profile.State != geology.Draft || profile.Version != 1 {
				return fmt.Errorf("import %s profile %s does not match preview", id, profileID)
			}
			if len(profile.Layers) != len(group.Layers) {
				return fmt.Errorf("import %s profile %s layers do not match preview", id, profileID)
			}
			for j, layer := range profile.Layers {
				if layer != group.Layers[j].Layer {
					return fmt.Errorf("import %s profile %s layers do not match preview", id, profileID)
				}
			}
		}
	default:
		return fmt.Errorf("unknown import status %s", id)
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
