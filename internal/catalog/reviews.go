package catalog

import (
	"context"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/correlation"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/geology"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/persistence"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/review"
	"sort"
	"strings"
	"time"
)

const (
	maxReviewThreads = 10000
	maxReviewEntries = 1000
)

// ReviewInput is one appended conclusion. Status defaults to draft; a
// replacement carries replaces and replace_reason.
type ReviewInput struct {
	ExpectedSequence int               `json:"expected_sequence"`
	Reviewer         string            `json:"reviewer"`
	Conclusion       string            `json:"conclusion"`
	DoubtIntervals   []review.Interval `json:"doubt_intervals"`
	Adoption         string            `json:"adoption"`
	Status           string            `json:"status"`
	Replaces         int               `json:"replaces"`
	ReplaceReason    string            `json:"replace_reason"`
}

type ConfirmInput struct {
	ExpectedSequence int `json:"expected_sequence"`
}

type MigrateInput struct {
	ExpectedSequence int    `json:"expected_sequence"`
	Reviewer         string `json:"reviewer"`
	Reason           string `json:"reason"`
}

// ThreadView is the serialized shape of a review thread, annotated with
// whether the result instance it was based on is still present.
type ThreadView struct {
	ID            string             `json:"id"`
	ComparisonID  string             `json:"comparison_id"`
	Fingerprint   string             `json:"fingerprint"`
	Entries       []review.Entry     `json:"entries"`
	Sequence      int                `json:"sequence"`
	Active        bool               `json:"active"`
	ResultExists  bool               `json:"result_exists"`
	ResultChanged bool               `json:"result_changed"`
	CreatedAt     time.Time          `json:"created_at"`
	UpdatedAt     time.Time          `json:"updated_at"`
	Origin        *review.Origin     `json:"origin,omitempty"`
	Migrations    []review.Migration `json:"migrations,omitempty"`
	CurrentThread string             `json:"current_thread,omitempty"`
}

type ReviewPage struct {
	ComparisonID  string       `json:"comparison_id"`
	ResultExists  bool         `json:"result_exists"`
	CurrentThread string       `json:"current_thread,omitempty"`
	Items         []ThreadView `json:"items"`
}

// SequenceConflict lets the loser of a concurrent append retrieve the current
// ordering without a second round trip.
type SequenceConflict struct {
	Detail   string     `json:"-"`
	Expected int        `json:"expected_sequence"`
	Actual   int        `json:"actual_sequence"`
	Thread   ThreadView `json:"thread"`
}

func (e *SequenceConflict) Error() string { return e.Detail }

func basisOf(result correlation.Result) review.Basis {
	return review.Basis{
		ResultFingerprint: result.Fingerprint,
		ResultCreatedAt:   result.CreatedAt,
		Algorithm:         result.Algorithm,
		Left: geology.RevisionRef{
			ID:      result.Request.Left.ID,
			Version: result.Request.Left.Version,
		},
		Right: geology.RevisionRef{
			ID:      result.Request.Right.ID,
			Version: result.Request.Right.Version,
		},
	}
}

// activeThread finds the thread matching the current result instance and the
// full set of threads for the comparison, ordered oldest first.
func threadsFor(state *persistence.State, comparisonID string) (active string, all []review.Thread) {
	result, exists := state.Comparisons[comparisonID]
	for _, t := range state.Reviews {
		if t.ComparisonID != comparisonID {
			continue
		}
		all = append(all, t)
		if exists && t.Fingerprint == result.Fingerprint {
			active = t.ID
		}
	}
	sort.Slice(all, func(i, j int) bool {
		if !all[i].CreatedAt.Equal(all[j].CreatedAt) {
			return all[i].CreatedAt.Before(all[j].CreatedAt)
		}
		return all[i].ID < all[j].ID
	})
	return active, all
}

func buildView(state persistence.State, t review.Thread) ThreadView {
	result, exists := state.Comparisons[t.ComparisonID]
	activeID, _ := threadsFor(&state, t.ComparisonID)
	view := ThreadView{
		ID:            t.ID,
		ComparisonID:  t.ComparisonID,
		Fingerprint:   t.Fingerprint,
		Entries:       append([]review.Entry{}, t.Entries...),
		Sequence:      len(t.Entries),
		Active:        exists && t.Fingerprint == result.Fingerprint,
		ResultExists:  exists,
		ResultChanged: exists && t.Fingerprint != result.Fingerprint,
		CreatedAt:     t.CreatedAt,
		UpdatedAt:     t.UpdatedAt,
		Origin:        t.Origin,
		Migrations:    append([]review.Migration{}, t.Migrations...),
		CurrentThread: activeID,
	}
	if view.Active {
		view.CurrentThread = ""
	}
	return view
}

func (s *Service) Reviews(ctx context.Context, comparisonID string) (ReviewPage, error) {
	if !geology.ValidID(comparisonID, "cmp_") {
		return ReviewPage{}, geology.Invalid("id", "对比结果编号无效")
	}
	page := ReviewPage{ComparisonID: comparisonID, Items: []ThreadView{}}
	err := s.repo.View(ctx, func(state persistence.State) error {
		_, all := threadsFor(&state, comparisonID)
		result, exists := state.Comparisons[comparisonID]
		page.ResultExists = exists
		if exists {
			for _, t := range all {
				if t.Fingerprint == result.Fingerprint {
					page.CurrentThread = t.ID
					break
				}
			}
		}
		for _, t := range all {
			page.Items = append(page.Items, buildView(state, t))
		}
		return nil
	})
	return page, err
}

func (s *Service) Review(ctx context.Context, threadID string) (ThreadView, error) {
	if !review.ValidThreadID(threadID) {
		return ThreadView{}, geology.Invalid("id", "复核记录编号无效")
	}
	var view ThreadView
	err := s.repo.View(ctx, func(state persistence.State) error {
		t, ok := state.Reviews[threadID]
		if !ok {
			return geology.Missing("复核记录不存在")
		}
		view = buildView(state, t)
		return nil
	})
	return view, err
}

func normalizeReviewInput(input *ReviewInput) error {
	input.Reviewer = strings.TrimSpace(input.Reviewer)
	input.Conclusion = strings.TrimSpace(input.Conclusion)
	input.ReplaceReason = strings.TrimSpace(input.ReplaceReason)
	if input.Status == "" {
		input.Status = review.Draft
	}
	if input.Adoption == "" {
		input.Adoption = review.Pending
	}
	if input.DoubtIntervals == nil {
		input.DoubtIntervals = []review.Interval{}
	}
	if err := review.ValidateEntryInput(input.Reviewer, input.Conclusion, input.DoubtIntervals, input.Adoption); err != nil {
		return err
	}
	if !review.ValidWritableStatus(input.Status) {
		return geology.Invalid("status", "追加的结论只能为 draft 或 confirmed")
	}
	if input.Replaces != 0 {
		if err := geology.Text("replace_reason", input.ReplaceReason, 1, 500); err != nil {
			return err
		}
	}
	return nil
}

// AddReview appends a conclusion to the active thread of a comparison,
// creating the thread on the first conclusion.
func (s *Service) AddReview(ctx context.Context, comparisonID string, input ReviewInput) (ThreadView, error) {
	if !geology.ValidID(comparisonID, "cmp_") {
		return ThreadView{}, geology.Invalid("id", "对比结果编号无效")
	}
	if err := normalizeReviewInput(&input); err != nil {
		return ThreadView{}, err
	}
	if input.ExpectedSequence < 0 {
		return ThreadView{}, geology.Invalid("expected_sequence", "预期序号不能为负数")
	}
	var view ThreadView
	err := s.repo.Update(ctx, func(state *persistence.State) (bool, error) {
		result, exists := state.Comparisons[comparisonID]
		if !exists {
			return false, geology.Missing("对比结果不存在；复核必须基于当前存在的结果")
		}
		activeID, _ := threadsFor(state, comparisonID)
		var thread review.Thread
		if activeID == "" {
			if input.ExpectedSequence != 0 {
				return false, geology.Invalid("expected_sequence", "当前结果实例没有可追加的复核线程；新线程预期序号必须为 0，或显式迁移旧复核")
			}
			if len(state.Reviews) >= maxReviewThreads {
				return false, geology.Conflict("复核线程数量达到上限")
			}
			id, err := review.NewID()
			if err != nil {
				return false, err
			}
			now := time.Now().UTC()
			thread = review.Thread{
				ID: id, ComparisonID: comparisonID, Fingerprint: result.Fingerprint,
				Entries: []review.Entry{}, CreatedAt: now, UpdatedAt: now,
			}
		} else {
			thread = state.Reviews[activeID]
			if input.ExpectedSequence != len(thread.Entries) {
				return false, &SequenceConflict{
					Detail:   "复核序号已变化，请读取最新结论顺序后重试",
					Expected: input.ExpectedSequence,
					Actual:   len(thread.Entries),
					Thread:   buildView(*state, thread),
				}
			}
			if len(thread.Entries) >= maxReviewEntries {
				return false, geology.Conflict("单条对比结果的复核结论达到 1000 条上限")
			}
		}
		if input.Replaces != 0 {
			if input.Replaces > len(thread.Entries) {
				return false, geology.Invalid("replaces", "被替代结论序号不存在")
			}
			target := thread.Entries[input.Replaces-1]
			if target.Status != review.Confirmed {
				return false, geology.Invalid("replaces", "只能替代已确认且未被替代的结论")
			}
		}
		now := time.Now().UTC()
		if !thread.UpdatedAt.IsZero() && now.Before(thread.UpdatedAt) {
			now = thread.UpdatedAt
		}
		entry := review.Entry{
			Sequence:       len(thread.Entries) + 1,
			Reviewer:       input.Reviewer,
			Conclusion:     input.Conclusion,
			DoubtIntervals: review.NormalizeIntervals(input.DoubtIntervals),
			Adoption:       input.Adoption,
			Status:         input.Status,
			Basis:          basisOf(result),
			Replaces:       input.Replaces,
			ReplaceReason:  input.ReplaceReason,
			CreatedAt:      now,
		}
		if input.Replaces != 0 {
			// Only one pending replacement may target a live confirmed entry,
			// whether it is appended as a draft or confirmed immediately.
			for _, e := range thread.Entries {
				if e.Status == review.Draft && e.Replaces == input.Replaces {
					return false, geology.Conflict("该结论已有草拟中的替代记录，请先确认或放弃")
				}
			}
		}
		if input.Status == review.Confirmed {
			live := thread.LiveConfirmed()
			if live != 0 {
				if input.Replaces == live {
					thread.Entries[live-1].Status = review.Superseded
				} else {
					return false, geology.Conflict("已有已确认结论；不能覆盖，只能追加一条针对它的替代记录")
				}
			} else if input.Replaces != 0 {
				return false, geology.Invalid("replaces", "当前没有可替代的已确认结论")
			}
		}
		thread.Entries = append(thread.Entries, entry)
		thread.UpdatedAt = now
		state.Reviews[thread.ID] = thread
		view = buildView(*state, thread)
		return true, nil
	})
	return view, err
}

// ConfirmReview promotes a draft entry to confirmed. When the draft is a
// replacement, its target is flipped to superseded in the same write.
func (s *Service) ConfirmReview(ctx context.Context, threadID string, sequence int, input ConfirmInput) (ThreadView, error) {
	if !review.ValidThreadID(threadID) {
		return ThreadView{}, geology.Invalid("id", "复核记录编号无效")
	}
	if sequence < 1 {
		return ThreadView{}, geology.Invalid("sequence", "结论序号必须为正整数")
	}
	if input.ExpectedSequence < 0 {
		return ThreadView{}, geology.Invalid("expected_sequence", "预期序号不能为负数")
	}
	var view ThreadView
	err := s.repo.Update(ctx, func(state *persistence.State) (bool, error) {
		thread, ok := state.Reviews[threadID]
		if !ok {
			return false, geology.Missing("复核记录不存在")
		}
		if input.ExpectedSequence != len(thread.Entries) {
			return false, &SequenceConflict{
				Detail:   "复核序号已变化，请读取最新结论顺序后重试",
				Expected: input.ExpectedSequence,
				Actual:   len(thread.Entries),
				Thread:   buildView(*state, thread),
			}
		}
		result, exists := state.Comparisons[thread.ComparisonID]
		if !exists || result.Fingerprint != thread.Fingerprint {
			return false, geology.Conflict("复核针对的结果实例已变化或不存在，不能在此线程上确认")
		}
		if sequence > len(thread.Entries) {
			return false, geology.Missing("结论序号不存在")
		}
		entry := thread.Entries[sequence-1]
		if entry.Status != review.Draft {
			return false, geology.Conflict("只有草拟结论可以确认")
		}
		now := time.Now().UTC()
		if now.Before(thread.UpdatedAt) {
			now = thread.UpdatedAt
		}
		if entry.Replaces != 0 {
			target := thread.Entries[entry.Replaces-1]
			if target.Status != review.Confirmed {
				return false, geology.Conflict("被替代结论已经不是已确认状态")
			}
			thread.Entries[entry.Replaces-1].Status = review.Superseded
		} else if thread.LiveConfirmed() != 0 {
			return false, geology.Conflict("已有已确认结论；草拟结论不能直接确认，请追加替代记录")
		}
		thread.Entries[sequence-1].Status = review.Confirmed
		thread.UpdatedAt = now
		state.Reviews[threadID] = thread
		view = buildView(*state, thread)
		return true, nil
	})
	return view, err
}

// MigrateReview carries a thread forward onto the current result instance of
// the same comparison. Conclusions are copied, never silently attached; both
// sides of the migration keep a link.
func (s *Service) MigrateReview(ctx context.Context, threadID string, input MigrateInput) (ThreadView, error) {
	if !review.ValidThreadID(threadID) {
		return ThreadView{}, geology.Invalid("id", "复核记录编号无效")
	}
	input.Reviewer = strings.TrimSpace(input.Reviewer)
	input.Reason = strings.TrimSpace(input.Reason)
	if err := geology.Text("reviewer", input.Reviewer, 1, 120); err != nil {
		return ThreadView{}, err
	}
	if err := geology.Text("reason", input.Reason, 1, 500); err != nil {
		return ThreadView{}, err
	}
	if input.ExpectedSequence < 0 {
		return ThreadView{}, geology.Invalid("expected_sequence", "预期序号不能为负数")
	}
	var view ThreadView
	err := s.repo.Update(ctx, func(state *persistence.State) (bool, error) {
		source, ok := state.Reviews[threadID]
		if !ok {
			return false, geology.Missing("复核记录不存在")
		}
		if input.ExpectedSequence != len(source.Entries) {
			return false, &SequenceConflict{
				Detail:   "复核序号已变化，请读取最新结论顺序后重试",
				Expected: input.ExpectedSequence,
				Actual:   len(source.Entries),
				Thread:   buildView(*state, source),
			}
		}
		if len(source.Migrations) > 0 {
			return false, geology.Conflict("该复核线程已经迁移过，请从最新一代线程继续迁移")
		}
		result, exists := state.Comparisons[source.ComparisonID]
		if !exists {
			return false, geology.Conflict("对比结果已经不存在，无法迁移到新结果")
		}
		if result.Fingerprint == source.Fingerprint {
			return false, geology.Conflict("该复核记录依据的仍是当前结果，无需迁移")
		}
		if activeID, _ := threadsFor(state, source.ComparisonID); activeID != "" {
			return false, geology.Conflict("当前结果实例已经存在复核线程，不能重复迁移")
		}
		if len(state.Reviews) >= maxReviewThreads {
			return false, geology.Conflict("复核线程数量达到上限")
		}
		id, err := review.NewID()
		if err != nil {
			return false, err
		}
		now := time.Now().UTC()
		basis := basisOf(result)
		entries := make([]review.Entry, 0, len(source.Entries))
		for _, e := range source.Entries {
			copied := e
			copied.Basis = basis
			copied.OriginSequence = e.Sequence
			entries = append(entries, copied)
		}
		target := review.Thread{
			ID:           id,
			ComparisonID: source.ComparisonID,
			Fingerprint:  result.Fingerprint,
			Entries:      entries,
			CreatedAt:    now,
			UpdatedAt:    now,
			Origin: &review.Origin{
				SourceThreadID: source.ID,
				MigratedAt:     now,
				Reviewer:       input.Reviewer,
				Reason:         input.Reason,
			},
		}
		source.Migrations = append(source.Migrations, review.Migration{
			TargetThreadID: id,
			MigratedAt:     now,
			Reviewer:       input.Reviewer,
			Reason:         input.Reason,
		})
		state.Reviews[source.ID] = source
		state.Reviews[id] = target
		view = buildView(*state, target)
		return true, nil
	})
	return view, err
}

// DeleteComparison removes a saved comparison result. Its review threads are
// retained and marked as relying on a result that no longer exists. The same
// inputs may later be computed again as a fresh result instance.
func (s *Service) DeleteComparison(ctx context.Context, id string) error {
	if !geology.ValidID(id, "cmp_") {
		return geology.Invalid("id", "对比结果编号无效")
	}
	return s.repo.Update(ctx, func(state *persistence.State) (bool, error) {
		if _, ok := state.Comparisons[id]; !ok {
			return false, geology.Missing("对比结果不存在")
		}
		delete(state.Comparisons, id)
		return true, nil
	})
}
