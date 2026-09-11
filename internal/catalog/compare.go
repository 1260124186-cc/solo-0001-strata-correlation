package catalog

import (
	"context"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/correlation"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/geology"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/persistence"
	"sort"
	"time"
)

type ComparisonPage struct {
	Items  []correlation.Result `json:"items"`
	Total  int                  `json:"total"`
	Offset int                  `json:"offset"`
	Limit  int                  `json:"limit"`
}

func (s *Service) Compare(ctx context.Context, input correlation.Request) (correlation.Result, bool, error) {
	if err := input.Validate(); err != nil {
		return correlation.Result{}, false, err
	}
	var result correlation.Result
	reused := false
	err := s.repo.Update(ctx, func(state *persistence.State) (bool, error) {
		if cached, ok := state.Comparisons[input.Key()]; ok {
			materialized, err := state.Materialize(cached)
			if err != nil {
				return false, err
			}
			result = materialized
			reused = true
			// Old snapshots still embed the per-segment detail. Compact it
			// only when the detail reproduces from the locked revisions;
			// otherwise keep the embedded record untouched and readable.
			if cached.IsLegacy() {
				if compact, ok := recompact(state, cached, input); ok {
					state.Comparisons[input.Key()] = compact
					return true, nil
				}
			}
			return false, nil
		}
		if len(state.Comparisons) >= 10000 {
			return false, geology.Conflict("对比结果数量达到 10000 条上限")
		}
		left, err := state.Revision(input.Left.ID, input.Left.Version)
		if err != nil {
			return false, err
		}
		right, err := state.Revision(input.Right.ID, input.Right.Version)
		if err != nil {
			return false, err
		}
		result, err = correlation.Align(left.Profile, right.Profile, input, time.Now().UTC())
		if err != nil {
			return false, err
		}
		state.Comparisons[result.ID] = correlation.NewRecord(result)
		return true, nil
	})
	return result, reused, err
}

// recompact turns a legacy record into a compact one only when recomputing
// from the referenced revisions reproduces the originally returned content
// (pinned by the record's content digest).
func recompact(state *persistence.State, cached correlation.Record, input correlation.Request) (correlation.Record, bool) {
	left, err := state.Revision(input.Left.ID, input.Left.Version)
	if err != nil {
		return correlation.Record{}, false
	}
	right, err := state.Revision(input.Right.ID, input.Right.Version)
	if err != nil {
		return correlation.Record{}, false
	}
	computed, err := correlation.Align(left.Profile, right.Profile, input, cached.CreatedAt)
	if err != nil || correlation.ContentDigest(computed) != cached.Summary.Digest {
		return correlation.Record{}, false
	}
	return correlation.NewRecord(computed), true
}

func (s *Service) Comparison(ctx context.Context, id string) (correlation.Result, error) {
	var result correlation.Result
	err := s.repo.View(ctx, func(state persistence.State) error {
		record, exists := state.Comparisons[id]
		if !exists {
			return geology.Missing("对比结果不存在")
		}
		materialized, err := state.Materialize(record)
		if err != nil {
			return err
		}
		result = materialized
		return nil
	})
	return result, err
}

func (s *Service) Comparisons(ctx context.Context, profile string, offset, limit int) (ComparisonPage, error) {
	if offset < 0 || offset > 1000000 || limit < 1 || limit > 100 {
		return ComparisonPage{}, geology.Invalid("pagination", "分页参数超出范围")
	}
	page := ComparisonPage{Items: []correlation.Result{}, Offset: offset, Limit: limit}
	err := s.repo.View(ctx, func(state persistence.State) error {
		if profile != "" {
			if _, err := state.Latest(profile); err != nil {
				return err
			}
		}
		records := make([]correlation.Record, 0)
		for _, record := range state.Comparisons {
			if profile != "" && record.Request.Left.ID != profile && record.Request.Right.ID != profile {
				continue
			}
			records = append(records, record)
		}
		sort.Slice(records, func(i, j int) bool {
			if !records[i].CreatedAt.Equal(records[j].CreatedAt) {
				return records[i].CreatedAt.After(records[j].CreatedAt)
			}
			return records[i].ID < records[j].ID
		})
		page.Total = len(records)
		start := min(offset, len(records))
		end := min(start+limit, len(records))
		for _, record := range records[start:end] {
			result, err := state.Materialize(record)
			if err != nil {
				return err
			}
			page.Items = append(page.Items, result)
		}
		return nil
	})
	return page, err
}
