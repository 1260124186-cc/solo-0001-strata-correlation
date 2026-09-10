package catalog

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/geology"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/persistence"
	"sort"
	"time"
)

type CreateAnnotation struct {
	Target geology.AnnotationTarget `json:"target"`
	Title  string                   `json:"title"`
	Body   string                   `json:"body"`
	Reason string                   `json:"reason"`
}

type ReviseAnnotation struct {
	Title  string `json:"title"`
	Body   string `json:"body"`
	Reason string `json:"reason"`
}

type ConfirmAnnotation struct {
	Reason string `json:"reason"`
}

type AnnotationPage struct {
	Items  []geology.AnnotationView `json:"items"`
	Total  int                      `json:"total"`
	Offset int                      `json:"offset"`
	Limit  int                      `json:"limit"`
}

func newAnnotationID() (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return "ant_" + hex.EncodeToString(bytes), nil
}

// nextAnnotationTime 保证同一注记修订时间单调不减。
func nextAnnotationTime(previous time.Time) time.Time {
	now := time.Now().UTC()
	if now.Before(previous) {
		return previous
	}
	return now
}

func (s *Service) CreateAnnotation(ctx context.Context, input CreateAnnotation) (geology.AnnotationView, error) {
	title, body, err := geology.NormalizeAnnotationContent(input.Title, input.Body)
	if err != nil {
		return geology.AnnotationView{}, err
	}
	reason, err := normalizedReason(input.Reason)
	if err != nil {
		return geology.AnnotationView{}, err
	}
	id, err := newAnnotationID()
	if err != nil {
		return geology.AnnotationView{}, err
	}
	var view geology.AnnotationView
	err = s.repo.Update(ctx, func(state *persistence.State) (bool, error) {
		if len(state.Annotations) >= geology.MaxAnnotations {
			return false, geology.Conflict("注记数量达到 10000 条上限")
		}
		if _, exists := state.Annotations[id]; exists {
			return false, geology.Conflict("注记编号重复，请重试")
		}
		target, err := state.Revision(input.Target.ProfileID, input.Target.Version)
		if err != nil {
			return false, err
		}
		history := state.Histories[input.Target.ProfileID]
		everSealed := false
		for _, revision := range history {
			if revision.Profile.State == geology.Sealed {
				everSealed = true
				break
			}
		}
		if !everSealed {
			return false, geology.Conflict("剖面尚未锁定，不能为其添加深度注记")
		}
		if err = geology.ValidateTarget(input.Target, target.Profile); err != nil {
			return false, err
		}
		now := time.Now().UTC()
		annotation := geology.Annotation{
			ID:     id,
			Target: input.Target,
			Revisions: []geology.AnnotationRevision{{
				Revision: 1,
				Title:    title,
				Body:     body,
				Status:   geology.AnnotationDraft,
				Action:   "create",
				Reason:   reason,
				At:       now,
			}},
			CreatedAt: now,
			UpdatedAt: now,
		}
		state.Annotations[id] = annotation
		view = geology.ViewOf(annotation, target.Profile)
		return true, nil
	})
	return view, err
}

func (s *Service) appendNoteRevision(ctx context.Context, id string, apply func(*geology.Annotation, time.Time) error) (geology.AnnotationView, error) {
	var view geology.AnnotationView
	err := s.repo.Update(ctx, func(state *persistence.State) (bool, error) {
		annotation, err := state.Annotation(id)
		if err != nil {
			return false, err
		}
		if len(annotation.Revisions) >= geology.MaxNoteRevisions {
			return false, geology.Conflict("单条注记最多保留 200 个修订")
		}
		at := nextAnnotationTime(annotation.UpdatedAt)
		if err = apply(&annotation, at); err != nil {
			return false, err
		}
		if err = annotation.Validate(); err != nil {
			return false, err
		}
		target, err := state.AnnotationTargetRevision(annotation.Target)
		if err != nil {
			return false, err
		}
		state.Annotations[id] = annotation
		view = geology.ViewOf(annotation, target.Profile)
		return true, nil
	})
	return view, err
}

// ReviseAnnotation 只在草拟链尾追加一条草拟修订；定稿内容不会被覆盖。
func (s *Service) ReviseAnnotation(ctx context.Context, id string, input ReviseAnnotation) (geology.AnnotationView, error) {
	title, body, err := geology.NormalizeAnnotationContent(input.Title, input.Body)
	if err != nil {
		return geology.AnnotationView{}, err
	}
	reason, err := normalizedReason(input.Reason)
	if err != nil {
		return geology.AnnotationView{}, err
	}
	return s.appendNoteRevision(ctx, id, func(a *geology.Annotation, at time.Time) error {
		latest := a.Latest()
		if latest.Status != geology.AnnotationDraft {
			return geology.Conflict("注记已定稿，请先重新打开再修改")
		}
		a.Revisions = append(a.Revisions, geology.AnnotationRevision{
			Revision: len(a.Revisions) + 1,
			Title:    title,
			Body:     body,
			Status:   geology.AnnotationDraft,
			Action:   "revise",
			Reason:   reason,
			At:       at,
		})
		a.UpdatedAt = at
		return nil
	})
}

// ConfirmAnnotation 追加确认修订，定稿后该修订不可再被改写。
func (s *Service) ConfirmAnnotation(ctx context.Context, id string, input ConfirmAnnotation) (geology.AnnotationView, error) {
	reason, err := normalizedReason(input.Reason)
	if err != nil {
		return geology.AnnotationView{}, err
	}
	return s.appendNoteRevision(ctx, id, func(a *geology.Annotation, at time.Time) error {
		latest := a.Latest()
		if latest.Status == geology.AnnotationConfirmed {
			return geology.Conflict("注记已经确认")
		}
		a.Revisions = append(a.Revisions, geology.AnnotationRevision{
			Revision: len(a.Revisions) + 1,
			Title:    latest.Title,
			Body:     latest.Body,
			Status:   geology.AnnotationConfirmed,
			Action:   "confirm",
			Reason:   reason,
			At:       at,
		})
		a.UpdatedAt = at
		return nil
	})
}

// ReopenAnnotation 重新打开已定稿注记：追加一条草拟修订，
// 已确认的修订原样保留，维护完整来龙去脉。
func (s *Service) ReopenAnnotation(ctx context.Context, id string, input ConfirmAnnotation) (geology.AnnotationView, error) {
	reason, err := normalizedReason(input.Reason)
	if err != nil {
		return geology.AnnotationView{}, err
	}
	return s.appendNoteRevision(ctx, id, func(a *geology.Annotation, at time.Time) error {
		latest := a.Latest()
		if latest.Status != geology.AnnotationConfirmed {
			return geology.Conflict("只能重新打开已经定稿的注记")
		}
		a.Revisions = append(a.Revisions, geology.AnnotationRevision{
			Revision: len(a.Revisions) + 1,
			Title:    latest.Title,
			Body:     latest.Body,
			Status:   geology.AnnotationDraft,
			Action:   "reopen",
			Reason:   reason,
			At:       at,
		})
		a.UpdatedAt = at
		return nil
	})
}

func (s *Service) Annotation(ctx context.Context, id string) (geology.AnnotationView, error) {
	var view geology.AnnotationView
	err := s.repo.View(ctx, func(state persistence.State) error {
		annotation, err := state.Annotation(id)
		if err != nil {
			return err
		}
		target, err := state.AnnotationTargetRevision(annotation.Target)
		if err != nil {
			return err
		}
		view = geology.ViewOf(annotation, target.Profile)
		return nil
	})
	return view, err
}

// Annotations 按锚定剖面和确认状态列出注记。注记独立于剖面版本，
// 不随剖面重新打开而消失或迁移。
func (s *Service) Annotations(ctx context.Context, profileID string, status geology.AnnotationStatus, offset, limit int) (AnnotationPage, error) {
	if offset < 0 || offset > 1000000 || limit < 1 || limit > 100 {
		return AnnotationPage{}, geology.Invalid("pagination", "分页参数超出范围")
	}
	if status != "" && !geology.ValidAnnotationStatus(status) {
		return AnnotationPage{}, geology.Invalid("status", "不支持的确认状态")
	}
	result := AnnotationPage{Items: []geology.AnnotationView{}, Offset: offset, Limit: limit}
	err := s.repo.View(ctx, func(state persistence.State) error {
		if profileID != "" {
			if _, err := state.Latest(profileID); err != nil {
				return err
			}
		}
		all := make([]geology.Annotation, 0)
		for _, annotation := range state.Annotations {
			if profileID != "" && annotation.Target.ProfileID != profileID {
				continue
			}
			if status != "" && annotation.Latest().Status != status {
				continue
			}
			all = append(all, annotation.Clone())
		}
		sort.Slice(all, func(i, j int) bool {
			if !all[i].UpdatedAt.Equal(all[j].UpdatedAt) {
				return all[i].UpdatedAt.After(all[j].UpdatedAt)
			}
			return all[i].ID < all[j].ID
		})
		result.Total = len(all)
		start := min(offset, len(all))
		end := min(start+limit, len(all))
		for _, annotation := range all[start:end] {
			target, err := state.AnnotationTargetRevision(annotation.Target)
			if err != nil {
				return err
			}
			result.Items = append(result.Items, geology.ViewOf(annotation, target.Profile))
		}
		return nil
	})
	return result, err
}
