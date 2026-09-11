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

// Compare 按指定算法版本创建或复用结果。不同算法版本编号不同，互不覆盖。
func (s *Service) Compare(ctx context.Context, input correlation.Input) (correlation.Result, bool, error) {
	request, algorithm, err := input.Resolve()
	if err != nil {
		return correlation.Result{}, false, err
	}
	var result correlation.Result
	reused := false
	err = s.repo.Update(ctx, func(state *persistence.State) (bool, error) {
		key := request.Key(algorithm)
		if cached, ok := state.Comparisons[key]; ok {
			result = cached.Clone()
			reused = true
			return false, nil
		}
		if len(state.Comparisons) >= 10000 {
			return false, geology.Conflict("对比结果数量达到 10000 条上限")
		}
		left, err := state.Revision(request.Left.ID, request.Left.Version)
		if err != nil {
			return false, err
		}
		right, err := state.Revision(request.Right.ID, request.Right.Version)
		if err != nil {
			return false, err
		}
		result, err = correlation.Align(left.Profile, right.Profile, request, algorithm, time.Now().UTC())
		if err != nil {
			return false, err
		}
		state.Comparisons[result.ID] = result.Clone()
		return true, nil
	})
	return result, reused, err
}

func (s *Service) Comparison(ctx context.Context, id string) (correlation.Result, error) {
	var result correlation.Result
	err := s.repo.View(ctx, func(state persistence.State) error {
		v, exists := state.Comparisons[id]
		if !exists {
			return geology.Missing("对比结果不存在")
		}
		result = v.Clone()
		return nil
	})
	return result, err
}

func (s *Service) Comparisons(ctx context.Context, profile, algorithm string, offset, limit int) (ComparisonPage, error) {
	if offset < 0 || offset > 1000000 || limit < 1 || limit > 100 {
		return ComparisonPage{}, geology.Invalid("pagination", "分页参数超出范围")
	}
	if algorithm != "" {
		if err := correlation.ValidateAlgorithm(algorithm); err != nil {
			return ComparisonPage{}, err
		}
	}
	result := ComparisonPage{Items: []correlation.Result{}, Offset: offset, Limit: limit}
	err := s.repo.View(ctx, func(state persistence.State) error {
		if profile != "" {
			if _, err := state.Latest(profile); err != nil {
				return err
			}
		}
		all := make([]correlation.Result, 0)
		for _, v := range state.Comparisons {
			if profile != "" && v.Request.Left.ID != profile && v.Request.Right.ID != profile {
				continue
			}
			if algorithm != "" && v.Algorithm != algorithm {
				continue
			}
			all = append(all, v.Clone())
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

// PreviewRecompute 不落盘地给出已保存结果与目标算法版本之间的差异说明。
func (s *Service) PreviewRecompute(ctx context.Context, id, target string) (correlation.Diff, correlation.Result, error) {
	if target == "" {
		target = correlation.Current
	}
	if err := correlation.ValidateAlgorithm(target); err != nil {
		return correlation.Diff{}, correlation.Result{}, err
	}
	var diff correlation.Diff
	var source correlation.Result
	err := s.repo.View(ctx, func(state persistence.State) error {
		saved, exists := state.Comparisons[id]
		if !exists {
			return geology.Missing("对比结果不存在")
		}
		source = saved.Clone()
		if saved.Algorithm == target {
			return geology.Conflict("该结果已经使用 " + target + "，没有需要重算的版本差异")
		}
		left, err := state.Revision(saved.Request.Left.ID, saved.Request.Left.Version)
		if err != nil {
			return err
		}
		right, err := state.Revision(saved.Request.Right.ID, saved.Request.Right.Version)
		if err != nil {
			return err
		}
		// 预览时间以原结果时间为基准，保证落盘重算与预览的唯一差别只来自算法。
		recomputed, err := correlation.Align(left.Profile, right.Profile, saved.Request, target, saved.CreatedAt)
		if err != nil {
			return err
		}
		diff = correlation.DiffOf(saved, recomputed)
		return nil
	})
	return diff, source, err
}

type RecomputeOutcome struct {
	Source correlation.Result `json:"source"`
	Result correlation.Result `json:"result"`
	Reused bool               `json:"reused"`
	Diff   correlation.Diff   `json:"diff"`
}

// Recompute 用目标算法版本为同一组输入生成新结果；原结果始终保留。
func (s *Service) Recompute(ctx context.Context, id, target string) (RecomputeOutcome, error) {
	if target == "" {
		target = correlation.Current
	}
	if err := correlation.ValidateAlgorithm(target); err != nil {
		return RecomputeOutcome{}, err
	}
	var outcome RecomputeOutcome
	err := s.repo.Update(ctx, func(state *persistence.State) (bool, error) {
		saved, exists := state.Comparisons[id]
		if !exists {
			return false, geology.Missing("对比结果不存在")
		}
		outcome.Source = saved.Clone()
		if saved.Algorithm == target {
			return false, geology.Conflict("该结果已经使用 " + target + "，无需重算")
		}
		key := saved.Request.Key(target)
		if cached, ok := state.Comparisons[key]; ok {
			outcome.Result = cached.Clone()
			outcome.Reused = true
			outcome.Diff = correlation.DiffOf(saved, cached)
			return false, nil
		}
		if len(state.Comparisons) >= 10000 {
			return false, geology.Conflict("对比结果数量达到 10000 条上限")
		}
		left, err := state.Revision(saved.Request.Left.ID, saved.Request.Left.Version)
		if err != nil {
			return false, err
		}
		right, err := state.Revision(saved.Request.Right.ID, saved.Request.Right.Version)
		if err != nil {
			return false, err
		}
		recomputed, err := correlation.Align(left.Profile, right.Profile, saved.Request, target, time.Now().UTC())
		if err != nil {
			return false, err
		}
		state.Comparisons[recomputed.ID] = recomputed.Clone()
		outcome.Result = recomputed
		outcome.Diff = correlation.DiffOf(saved, recomputed)
		return true, nil
	})
	return outcome, err
}
