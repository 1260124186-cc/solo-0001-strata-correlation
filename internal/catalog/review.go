package catalog

import (
	"context"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/geology"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/persistence"
	"sort"
	"time"
)

const maxReviews = 20000

// ReviewInput creates or reuses an immutable quality review for one explicit
// profile revision.
type ReviewInput struct {
	Target   geology.Reference   `json:"target"`
	Partners []geology.Reference `json:"partners"`
	Reason   string              `json:"reason"`
}

type ReviewPage struct {
	Items  []geology.ReviewResult `json:"items"`
	Total  int                    `json:"total"`
	Offset int                    `json:"offset"`
	Limit  int                    `json:"limit"`
}

// Review runs the quality rules against an explicit revision and stores the
// conclusion as independent, immutable material. Repeating the same request
// returns the stored conclusion; later profile edits append new revisions and
// never reach into this result.
func (s *Service) Review(ctx context.Context, input ReviewInput) (geology.ReviewResult, bool, error) {
	if input.Partners == nil {
		return geology.ReviewResult{}, false, geology.Invalid("partners", "必须提供配对版本数组，无配对时使用 []")
	}
	reason, err := normalizedReason(input.Reason)
	if err != nil {
		return geology.ReviewResult{}, false, err
	}
	partners, err := geology.CanonicalPartners(input.Partners)
	if err != nil {
		return geology.ReviewResult{}, false, err
	}
	request := geology.ReviewRequest{Target: input.Target, Partners: partners}
	if err = request.Validate(); err != nil {
		return geology.ReviewResult{}, false, err
	}
	var result geology.ReviewResult
	reused := false
	err = s.repo.Update(ctx, func(state *persistence.State) (bool, error) {
		if cached, ok := state.Reviews[request.Key()]; ok {
			result = cached.Clone()
			reused = true
			return false, nil
		}
		if len(state.Reviews) >= maxReviews {
			return false, geology.Conflict("审查结论数量达到 20000 条上限")
		}
		target, err := state.Revision(request.Target.ID, request.Target.Version)
		if err != nil {
			return false, err
		}
		profiles := make([]geology.Profile, len(request.Partners))
		for i, ref := range request.Partners {
			revision, err := state.Revision(ref.ID, ref.Version)
			if err != nil {
				return false, err
			}
			profiles[i] = revision.Profile
		}
		result, err = geology.Review(target.Profile, profiles, request, reason, time.Now().UTC())
		if err != nil {
			return false, err
		}
		state.Reviews[result.ID] = result.Clone()
		return true, nil
	})
	return result, reused, err
}

// requirePassingReview enforces the lock-time quality gate. Reviews are
// immutable material keyed by target revision and partner set, so the latest
// conclusion for exactly this revision decides: a missing conclusion requires
// an explicit review run, and the latest failed conclusion is returned as
// evidence.
func requirePassingReview(state persistence.State, id string, version int) error {
	var latest *geology.ReviewResult
	for _, review := range state.Reviews {
		if review.Target.ID != id || review.Target.Version != version {
			continue
		}
		r := review.Clone()
		if latest == nil || r.CreatedAt.After(latest.CreatedAt) || (r.CreatedAt.Equal(latest.CreatedAt) && r.ID > latest.ID) {
			latest = &r
		}
	}
	if latest == nil {
		return geology.ReviewGate("该版本尚未完成质量审查，请先创建审查结论")
	}
	if !latest.Passed {
		return &geology.ReviewRejected{Review: *latest}
	}
	return nil
}

// ReviewRecord reads one stored review conclusion.
func (s *Service) ReviewRecord(ctx context.Context, id string) (geology.ReviewResult, error) {
	var result geology.ReviewResult
	err := s.repo.View(ctx, func(state persistence.State) error {
		review, exists := state.Reviews[id]
		if !exists {
			return geology.Missing("审查结论不存在")
		}
		result = review.Clone()
		return nil
	})
	return result, err
}

// Reviews lists stored conclusions, optionally filtered to one profile and
// revision, newest first.
func (s *Service) Reviews(ctx context.Context, profile string, version, offset, limit int) (ReviewPage, error) {
	if offset < 0 || offset > 1000000 || limit < 1 || limit > 100 {
		return ReviewPage{}, geology.Invalid("pagination", "分页参数超出范围")
	}
	if version < 0 || version > 500 {
		return ReviewPage{}, geology.Invalid("version", "版本参数超出范围")
	}
	result := ReviewPage{Items: []geology.ReviewResult{}, Offset: offset, Limit: limit}
	err := s.repo.View(ctx, func(state persistence.State) error {
		if profile != "" {
			if _, err := state.Latest(profile); err != nil {
				return err
			}
		}
		all := make([]geology.ReviewResult, 0)
		for _, review := range state.Reviews {
			if profile != "" && review.Target.ID != profile {
				continue
			}
			if version != 0 && review.Target.Version != version {
				continue
			}
			all = append(all, review.Clone())
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
