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
	Items  []correlation.Report `json:"items"`
	Total  int                  `json:"total"`
	Offset int                  `json:"offset"`
	Limit  int                  `json:"limit"`
}

type RefreshOutcome struct {
	Result     correlation.Report     `json:"result"`
	Difference correlation.Difference `json:"difference"`
}

func (s *Service) Compare(ctx context.Context, input correlation.Request) (correlation.Result, bool, error) {
	if err := input.Validate(); err != nil {
		return correlation.Result{}, false, err
	}
	var result correlation.Result
	reused := false
	err := s.repo.Update(ctx, func(state *persistence.State) (bool, error) {
		if cached, ok := state.Comparisons[input.Key()]; ok {
			result = cached.Clone()
			reused = true
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
		state.Comparisons[result.ID] = result.Clone()
		return true, nil
	})
	return result, reused, err
}

// currencyOf derives the read-time staleness view from the latest version of
// each referenced profile. Referenced profiles always exist in a validated
// state, so an error here means the snapshot is corrupt.
func currencyOf(state persistence.State, result correlation.Result) (correlation.Currency, error) {
	left, err := state.Latest(result.Request.Left.ID)
	if err != nil {
		return correlation.Currency{}, err
	}
	right, err := state.Latest(result.Request.Right.ID)
	if err != nil {
		return correlation.Currency{}, err
	}
	return result.Request.CurrencyOf(left.Version, right.Version), nil
}

func (s *Service) Comparison(ctx context.Context, id string) (correlation.Report, error) {
	var report correlation.Report
	err := s.repo.View(ctx, func(state persistence.State) error {
		v, exists := state.Comparisons[id]
		if !exists {
			return geology.Missing("对比结果不存在")
		}
		currency, err := currencyOf(state, v)
		if err != nil {
			return err
		}
		report = correlation.Report{Result: v.Clone(), Currency: currency}
		return nil
	})
	return report, err
}

func (s *Service) Comparisons(ctx context.Context, profile string, offset, limit int) (ComparisonPage, error) {
	if offset < 0 || offset > 1000000 || limit < 1 || limit > 100 {
		return ComparisonPage{}, geology.Invalid("pagination", "分页参数超出范围")
	}
	result := ComparisonPage{Items: []correlation.Report{}, Offset: offset, Limit: limit}
	err := s.repo.View(ctx, func(state persistence.State) error {
		if profile != "" {
			if _, err := state.Latest(profile); err != nil {
				return err
			}
		}
		all := make([]correlation.Report, 0)
		for _, v := range state.Comparisons {
			if profile != "" && v.Request.Left.ID != profile && v.Request.Right.ID != profile {
				continue
			}
			currency, err := currencyOf(state, v)
			if err != nil {
				return err
			}
			all = append(all, correlation.Report{Result: v.Clone(), Currency: currency})
		}
		sort.Slice(all, func(i, j int) bool {
			if !all[i].CreatedAt.Equal(all[j].CreatedAt) {
				return all[i].CreatedAt.After(all[j].CreatedAt)
			}
			return all[i].ID < all[j].ID
		})
		result.Total = len(all)
		start := min(offset, len(all))
		end := min(start+limit, len(all))
		result.Items = all[start:end]
		return nil
	})
	return result, err
}

// Refresh regenerates a comparison from the same input (both profiles and
// the offset) against the latest version of each profile. The origin result
// stays untouched; the new result has its own identity and records the
// origin in Supersedes. When a result for the refreshed input already exists
// it is reused instead of duplicated.
func (s *Service) Refresh(ctx context.Context, id string) (RefreshOutcome, bool, error) {
	var outcome RefreshOutcome
	reused := false
	err := s.repo.Update(ctx, func(state *persistence.State) (bool, error) {
		origin, exists := state.Comparisons[id]
		if !exists {
			return false, geology.Missing("对比结果不存在")
		}
		left, err := state.Latest(origin.Request.Left.ID)
		if err != nil {
			return false, err
		}
		right, err := state.Latest(origin.Request.Right.ID)
		if err != nil {
			return false, err
		}
		if left.Version == origin.Request.Left.Version && right.Version == origin.Request.Right.Version {
			return false, geology.Conflict("对比结果已基于最新版本，无需重新生成")
		}
		if left.State != geology.Sealed || right.State != geology.Sealed {
			return false, geology.Conflict("剖面的最新版本尚未锁定，不能重新生成对比")
		}
		next := correlation.Request{
			Left:     correlation.Reference{ID: left.ID, Version: left.Version},
			Right:    correlation.Reference{ID: right.ID, Version: right.Version},
			OffsetMM: origin.Request.OffsetMM,
		}
		if next.Left == next.Right {
			return false, geology.Conflict("两侧剖面的最新版本相同，无法重新生成对比")
		}
		var current correlation.Result
		changed := false
		if cached, ok := state.Comparisons[next.Key()]; ok {
			current = cached.Clone()
			reused = true
		} else {
			if len(state.Comparisons) >= 10000 {
				return false, geology.Conflict("对比结果数量达到 10000 条上限")
			}
			current, err = correlation.Align(left, right, next, time.Now().UTC())
			if err != nil {
				return false, err
			}
			current.Supersedes = origin.ID
			state.Comparisons[current.ID] = current.Clone()
			changed = true
		}
		currency, err := currencyOf(*state, current)
		if err != nil {
			return false, err
		}
		outcome = RefreshOutcome{
			Result:     correlation.Report{Result: current.Clone(), Currency: currency},
			Difference: correlation.Diff(origin, current),
		}
		return changed, nil
	})
	return outcome, reused, err
}
