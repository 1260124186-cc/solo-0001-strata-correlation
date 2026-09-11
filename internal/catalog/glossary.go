package catalog

import (
	"context"
	"sort"
	"strconv"
	"strings"

	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/geology"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/persistence"
)

// Glossary merge policies. The batch is validated as a whole before anything
// is written, so a conflict under "reject" never leaves a half-merged table.
const (
	// GlossaryPolicyReject aborts the whole batch when any key already
	// exists or repeats within the batch (after the batch is collapsed to
	// one entry per key).
	GlossaryPolicyReject = "reject"
	// GlossaryPolicyReplace overwrites existing entries and accepts the
	// last spelling within the batch.
	GlossaryPolicyReplace = "replace"
)

type GlossaryMerge struct {
	Policy  string                  `json:"policy"`
	Entries []geology.GlossaryInput `json:"entries"`
}

type GlossaryEntryOutcome struct {
	Key           string `json:"key"`
	CanonicalName string `json:"canonical_name"`
	Status        string `json:"status"` // created | replaced | unchanged
}

type GlossaryMergeResult struct {
	Policy    string                 `json:"policy"`
	Created   int                    `json:"created"`
	Replaced  int                    `json:"replaced"`
	Unchanged int                    `json:"unchanged"`
	Entries   []GlossaryEntryOutcome `json:"entries"`
}

type GlossaryPage struct {
	Items  []geology.GlossaryEntry `json:"items"`
	Total  int                     `json:"total"`
	Offset int                     `json:"offset"`
	Limit  int                     `json:"limit"`
}

type LegacyEquivalence struct {
	Key       string   `json:"key"`
	Spellings []string `json:"spellings"`
}

type LegacyProfileEquivalences struct {
	ProfileID    string              `json:"profile_id"`
	Equivalences []LegacyEquivalence `json:"equivalences"`
}

type LegacyReport struct {
	// Profiles lists, per profile, marker keys whose distinct raw spellings
	// already coexisted in historical revisions. They keep matching under
	// the unified rule and are grandfathered in duplicate validation.
	Profiles []LegacyProfileEquivalences `json:"profiles"`
}

// MergeGlossary imports an external word list atomically. Every entry is
// normalized and validated first; under "reject" a single conflict rejects
// the whole request, and under "replace" the whole batch is committed in one
// snapshot write. Nothing partial is ever persisted.
func (s *Service) MergeGlossary(ctx context.Context, input GlossaryMerge) (GlossaryMergeResult, error) {
	if input.Policy == "" {
		input.Policy = GlossaryPolicyReject
	}
	if input.Policy != GlossaryPolicyReject && input.Policy != GlossaryPolicyReplace {
		return GlossaryMergeResult{}, geology.Invalid("policy", "策略必须为 reject 或 replace")
	}
	if len(input.Entries) == 0 {
		return GlossaryMergeResult{}, geology.Invalid("entries", "至少提供一个词条")
	}
	if len(input.Entries) > geology.MaxGlossaryEntries {
		return GlossaryMergeResult{}, geology.Invalid("entries", "单次并入词条超过 5000 条上限")
	}
	// Collapse the batch to one entry per equivalence key; later spellings
	// win. The raw collisions are reported so "reject" stays explainable.
	collapsed := map[string]geology.GlossaryEntry{}
	order := make([]string, 0)
	for i, raw := range input.Entries {
		entry, err := geology.NormalizeGlossaryInput(raw)
		if err != nil {
			problem := err.(*geology.Problem)
			return GlossaryMergeResult{}, geology.Invalid("entries["+strconv.Itoa(i)+"]."+problem.Field, problem.Detail)
		}
		if _, exists := collapsed[entry.Key]; !exists {
			order = append(order, entry.Key)
		}
		collapsed[entry.Key] = entry
	}
	result := GlossaryMergeResult{Policy: input.Policy, Entries: []GlossaryEntryOutcome{}}
	err := s.repo.Update(ctx, func(state *persistence.State) (bool, error) {
		outcomes := make([]GlossaryEntryOutcome, 0, len(order))
		next := map[string]geology.GlossaryEntry{}
		newKeys := 0
		for key, entry := range state.MarkerGlossary {
			next[key] = entry
		}
		for _, key := range order {
			if _, exists := state.MarkerGlossary[key]; !exists {
				newKeys++
			}
		}
		if len(next)+newKeys > geology.MaxGlossaryEntries {
			return false, geology.Conflict("并入后标志层词条将超过 5000 条上限")
		}
		created, replaced, unchanged := 0, 0, 0
		for _, key := range order {
			entry := collapsed[key]
			existing, exists := state.MarkerGlossary[key]
			if exists && input.Policy == GlossaryPolicyReject {
				return false, geology.Conflict("词条 " + existing.CanonicalName + " 已存在，冲突策略为 reject")
			}
			status := "created"
			switch {
			case !exists:
				created++
			case existing.CanonicalName != entry.CanonicalName || existing.Note != entry.Note:
				replaced++
				status = "replaced"
			default:
				unchanged++
				status = "unchanged"
			}
			next[key] = entry
			outcomes = append(outcomes, GlossaryEntryOutcome{Key: key, CanonicalName: entry.CanonicalName, Status: status})
		}
		state.MarkerGlossary = next
		result.Created, result.Replaced, result.Unchanged = created, replaced, unchanged
		result.Entries = outcomes
		return true, nil
	})
	return result, err
}

func (s *Service) ListGlossary(ctx context.Context, q string, offset, limit int) (GlossaryPage, error) {
	if offset < 0 || offset > 1000000 || limit < 1 || limit > 100 {
		return GlossaryPage{}, geology.Invalid("pagination", "分页参数超出范围")
	}
	q = strings.TrimSpace(q)
	page := GlossaryPage{Items: []geology.GlossaryEntry{}, Offset: offset, Limit: limit}
	err := s.repo.View(ctx, func(state persistence.State) error {
		all := make([]geology.GlossaryEntry, 0, len(state.MarkerGlossary))
		for _, entry := range geology.SortedGlossary(state.MarkerGlossary) {
			if q != "" && !strings.Contains(strings.ToLower(entry.CanonicalName), strings.ToLower(q)) {
				continue
			}
			all = append(all, entry)
		}
		page.Total = len(all)
		start := min(offset, len(all))
		end := min(start+limit, len(all))
		page.Items = all[start:end]
		return nil
	})
	return page, err
}

// LegacyMarkerReport explains which already-saved names are equivalent under
// the unified rule but were catalogued with different spellings. Nothing is
// rewritten; the report is derived from the immutable revisions.
func (s *Service) LegacyMarkerReport(ctx context.Context) (LegacyReport, error) {
	report := LegacyReport{Profiles: []LegacyProfileEquivalences{}}
	err := s.repo.View(ctx, func(state persistence.State) error {
		ids := make([]string, 0, len(state.Histories))
		for id := range state.Histories {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			legacy := state.LegacyMarkers(id)
			if len(legacy) == 0 {
				continue
			}
			profileReport := LegacyProfileEquivalences{ProfileID: id, Equivalences: []LegacyEquivalence{}}
			for _, key := range legacy.Keys() {
				profileReport.Equivalences = append(profileReport.Equivalences, LegacyEquivalence{Key: key, Spellings: legacy.Spellings(key)})
			}
			report.Profiles = append(report.Profiles, profileReport)
		}
		return nil
	})
	return report, err
}
