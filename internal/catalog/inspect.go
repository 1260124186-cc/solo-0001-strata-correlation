package catalog

import (
	"context"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/correlation"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/geology"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/persistence"
)

func (s *Service) Point(ctx context.Context, id string, version int, depth int64) (geology.Point, error) {
	if version < 0 {
		return geology.Point{}, geology.Invalid("version", "版本不能为负数")
	}
	var result geology.Point
	err := s.repo.View(ctx, func(state persistence.State) error {
		var p geology.Profile
		var err error
		if version == 0 {
			p, err = state.Latest(id)
		} else {
			var revision geology.Revision
			revision, err = state.Revision(id, version)
			p = revision.Profile
		}
		if err != nil {
			return err
		}
		result, err = geology.AtDepth(p, depth)
		return err
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
		result, err = geology.DifferenceOf(a.Profile, b.Profile)
		return err
	})
	return result, err
}
