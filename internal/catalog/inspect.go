package catalog

import (
	"context"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/correlation"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/geology"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/persistence"
)

// PointOptions 指定查询的修订线或具体历史版本。Version 优先于 Branch。
type PointOptions struct {
	ID      string
	Branch  string
	Version int
	Depth   int64
}

func (s *Service) Point(ctx context.Context, opt PointOptions) (geology.Point, error) {
	if opt.Version < 0 {
		return geology.Point{}, geology.Invalid("version", "版本不能为负数")
	}
	branch, err := resolveBranch(opt.Branch)
	if err != nil {
		return geology.Point{}, err
	}
	var result geology.Point
	err = s.repo.View(ctx, func(state persistence.State) error {
		var p geology.Profile
		if opt.Version > 0 {
			revision, viewErr := state.Revision(opt.ID, opt.Version)
			if viewErr != nil {
				return viewErr
			}
			p = revision.Profile
		} else {
			head, viewErr := state.Head(opt.ID, branch)
			if viewErr != nil {
				return viewErr
			}
			p = head
		}
		var atErr error
		result, atErr = geology.AtDepth(p, opt.Depth)
		return atErr
	})
	return result, err
}

func (s *Service) SuggestOffset(ctx context.Context, input correlation.OffsetRequest) (correlation.OffsetProposal, error) {
	request := correlation.Request{Left: input.Left, Right: input.Right}
	if err := request.Validate(); err != nil {
		return correlation.OffsetProposal{}, err
	}
	var proposal correlation.OffsetProposal
	err := s.repo.View(ctx, func(state persistence.State) error {
		left, err := state.Revision(input.Left.ID, input.Left.Version)
		if err != nil {
			return err
		}
		right, err := state.Revision(input.Right.ID, input.Right.Version)
		if err != nil {
			return err
		}
		proposal, err = correlation.Suggest(left.Profile, right.Profile, input)
		return err
	})
	return proposal, err
}

// Difference 比较同一剖面的任意两个真实历史版本（可跨分叉线、无版本先后要求）。
func (s *Service) Difference(ctx context.Context, id string, from, to int) (geology.Difference, error) {
	var result geology.Difference
	err := s.repo.View(ctx, func(state persistence.State) error {
		a, err := state.Revision(id, from)
		if err != nil {
			return err
		}
		b, err := state.Revision(id, to)
		if err != nil {
			return err
		}
		if from == to {
			return geology.Invalid("version", "必须选择两个不同的版本")
		}
		result = geology.ContentDifference(a.Profile, b.Profile)
		return nil
	})
	return result, err
}
