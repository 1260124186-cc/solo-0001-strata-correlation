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
	Items  []correlation.View `json:"items"`
	Total  int                `json:"total"`
	Offset int                `json:"offset"`
	Limit  int                `json:"limit"`
}

// viewOf augments a stored result with its freshness against the current
// profile versions. The stored result itself is never modified.
func viewOf(state persistence.State, result correlation.Result) correlation.View {
	view := correlation.View{Result: result}
	sides := []struct {
		reference correlation.Reference
		status    *correlation.SideStatus
	}{
		{result.Request.Left, &view.Freshness.Left},
		{result.Request.Right, &view.Freshness.Right},
	}
	for _, side := range sides {
		history, ok := state.Histories[side.reference.ID]
		if !ok || len(history) == 0 {
			continue
		}
		side.status.ReferencedVersion = side.reference.Version
		side.status.CurrentVersion = len(history)
		side.status.LatestSealedVersion = latestSealed(history)
		side.status.Stale = side.reference.Version != side.status.CurrentVersion
		view.Freshness.Stale = view.Freshness.Stale || side.status.Stale
	}
	left, right := view.Freshness.Left, view.Freshness.Right
	advanced := left.LatestSealedVersion != left.ReferencedVersion || right.LatestSealedVersion != right.ReferencedVersion
	collapsed := result.Request.Left.ID == result.Request.Right.ID && left.LatestSealedVersion == right.LatestSealedVersion
	view.Freshness.Regeneratable = advanced && !collapsed
	for id, candidate := range state.Comparisons {
		if candidate.Supersedes == result.ID {
			view.Freshness.SupersededBy = append(view.Freshness.SupersededBy, id)
		}
	}
	sort.Slice(view.Freshness.SupersededBy, func(i, j int) bool {
		a, b := state.Comparisons[view.Freshness.SupersededBy[i]], state.Comparisons[view.Freshness.SupersededBy[j]]
		if !a.CreatedAt.Equal(b.CreatedAt) {
			return a.CreatedAt.Before(b.CreatedAt)
		}
		return a.ID < b.ID
	})
	return view
}

func latestSealed(history []geology.Revision) int {
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Profile.State == geology.Sealed {
			return history[i].Profile.Version
		}
	}
	return 0
}

func (s *Service) Compare(ctx context.Context, input correlation.Request) (correlation.View, bool, error) {
	if err := input.Validate(); err != nil {
		return correlation.View{}, false, err
	}
	var view correlation.View
	reused := false
	err := s.repo.Update(ctx, func(state *persistence.State) (bool, error) {
		if cached, ok := state.Comparisons[input.Key()]; ok {
			view = viewOf(*state, cached.Clone())
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
		result, err := correlation.Align(left.Profile, right.Profile, input, time.Now().UTC())
		if err != nil {
			return false, err
		}
		state.Comparisons[result.ID] = result.Clone()
		view = viewOf(*state, result)
		return true, nil
	})
	return view, reused, err
}

// Regenerate recomputes an existing comparison against the latest sealed
// version of each side with the same offset. The new result has its own
// identity and records the superseded result; the old result stays untouched.
func (s *Service) Regenerate(ctx context.Context, id string) (correlation.View, correlation.ResultDiff, bool, error) {
	var view correlation.View
	var diff correlation.ResultDiff
	reused := false
	err := s.repo.Update(ctx, func(state *persistence.State) (bool, error) {
		source, ok := state.Comparisons[id]
		if !ok {
			return false, geology.Missing("对比结果不存在")
		}
		leftHistory, ok := state.Histories[source.Request.Left.ID]
		if !ok {
			return false, geology.Missing("剖面不存在")
		}
		rightHistory, ok := state.Histories[source.Request.Right.ID]
		if !ok {
			return false, geology.Missing("剖面不存在")
		}
		target := correlation.Request{
			Left:     correlation.Reference{ID: source.Request.Left.ID, Version: latestSealed(leftHistory)},
			Right:    correlation.Reference{ID: source.Request.Right.ID, Version: latestSealed(rightHistory)},
			OffsetMM: source.Request.OffsetMM,
		}
		if target.Left.Version == 0 || target.Right.Version == 0 {
			return false, geology.Conflict("剖面缺少锁定版本，无法重新生成")
		}
		if target.Left == target.Right {
			return false, geology.Conflict("两侧解析到同一最新锁定版本，无法重新生成")
		}
		if target == source.Request {
			return false, geology.Conflict("没有更新的锁定版本，无法重新生成")
		}
		key := correlation.KeyFor(target, source.ID)
		if existing, ok := state.Comparisons[key]; ok {
			view = viewOf(*state, existing.Clone())
			diff = correlation.DiffResults(source, existing)
			reused = true
			return false, nil
		}
		if len(state.Comparisons) >= 10000 {
			return false, geology.Conflict("对比结果数量达到 10000 条上限")
		}
		left, err := state.Revision(target.Left.ID, target.Left.Version)
		if err != nil {
			return false, err
		}
		right, err := state.Revision(target.Right.ID, target.Right.Version)
		if err != nil {
			return false, err
		}
		result, err := correlation.Align(left.Profile, right.Profile, target, time.Now().UTC())
		if err != nil {
			return false, err
		}
		result.ID = key
		result.Supersedes = source.ID
		state.Comparisons[result.ID] = result.Clone()
		view = viewOf(*state, result)
		diff = correlation.DiffResults(source, result)
		return true, nil
	})
	return view, diff, reused, err
}

func (s *Service) Comparison(ctx context.Context, id string) (correlation.View, error) {
	var view correlation.View
	err := s.repo.View(ctx, func(state persistence.State) error {
		v, exists := state.Comparisons[id]
		if !exists {
			return geology.Missing("对比结果不存在")
		}
		view = viewOf(state, v.Clone())
		return nil
	})
	return view, err
}

func (s *Service) Comparisons(ctx context.Context, profile string, offset, limit int) (ComparisonPage, error) {
	if offset < 0 || offset > 1000000 || limit < 1 || limit > 100 {
		return ComparisonPage{}, geology.Invalid("pagination", "分页参数超出范围")
	}
	result := ComparisonPage{Items: []correlation.View{}, Offset: offset, Limit: limit}
	err := s.repo.View(ctx, func(state persistence.State) error {
		if profile != "" {
			if _, err := state.Latest(profile); err != nil {
				return err
			}
		}
		all := make([]correlation.View, 0)
		for _, v := range state.Comparisons {
			if profile != "" && v.Request.Left.ID != profile && v.Request.Right.ID != profile {
				continue
			}
			all = append(all, viewOf(state, v.Clone()))
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
