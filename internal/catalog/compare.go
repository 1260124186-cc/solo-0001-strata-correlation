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

func (s *Service) Comparisons(ctx context.Context, profile string, offset, limit int) (ComparisonPage, error) {
	if offset < 0 || offset > 1000000 || limit < 1 || limit > 100 {
		return ComparisonPage{}, geology.Invalid("pagination", "分页参数超出范围")
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
