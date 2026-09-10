package catalog

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/correlation"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/geology"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/persistence"
	"sort"
	"time"
)

const maxGroups = 1000

type GroupPage struct {
	Items  []correlation.Group `json:"items"`
	Total  int                 `json:"total"`
	Offset int                 `json:"offset"`
	Limit  int                 `json:"limit"`
}

func newGroupID() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return "grp_" + hex.EncodeToString(raw), nil
}

func (s *Service) CreateGroup(ctx context.Context, input correlation.GroupRequest) (correlation.Group, error) {
	if err := input.Validate(); err != nil {
		return correlation.Group{}, err
	}
	id, err := newGroupID()
	if err != nil {
		return correlation.Group{}, err
	}
	now := time.Now().UTC()
	items := make([]correlation.GroupItem, len(input.Targets))
	for i, target := range input.Targets {
		items[i] = correlation.GroupItem{Index: i, Target: target.Target, OffsetMM: target.OffsetMM, Status: correlation.ItemQueued}
	}
	group := correlation.Group{ID: id, Reference: input.Reference, Status: correlation.GroupQueued, Items: items, CreatedAt: now, UpdatedAt: now}
	err = s.repo.Update(ctx, func(state *persistence.State) (bool, error) {
		if state.Groups == nil {
			state.Groups = map[string]correlation.Group{}
		}
		if len(state.Groups) >= maxGroups {
			return false, geology.Conflict("对比组数量达到 1000 组上限")
		}
		if _, exists := state.Groups[id]; exists {
			return false, geology.Conflict("对比组编号重复，请重试")
		}
		state.Groups[id] = group.Clone()
		return true, nil
	})
	if err != nil {
		return correlation.Group{}, err
	}
	indexes := make([]int, len(items))
	for i := range items {
		indexes[i] = i
	}
	s.runner.enqueue(id, indexes)
	return group, nil
}

func (s *Service) Group(ctx context.Context, id string) (correlation.Group, error) {
	var group correlation.Group
	err := s.repo.View(ctx, func(state persistence.State) error {
		value, exists := state.Groups[id]
		if !exists {
			return geology.Missing("对比组不存在")
		}
		group = value.Clone()
		return nil
	})
	return group, err
}

func (s *Service) Groups(ctx context.Context, offset, limit int) (GroupPage, error) {
	if offset < 0 || offset > 1000000 || limit < 1 || limit > 100 {
		return GroupPage{}, geology.Invalid("pagination", "分页参数超出范围")
	}
	page := GroupPage{Items: []correlation.Group{}, Offset: offset, Limit: limit}
	err := s.repo.View(ctx, func(state persistence.State) error {
		all := make([]correlation.Group, 0, len(state.Groups))
		for _, group := range state.Groups {
			all = append(all, group.Clone())
		}
		sort.Slice(all, func(i, j int) bool {
			if !all[i].CreatedAt.Equal(all[j].CreatedAt) {
				return all[i].CreatedAt.After(all[j].CreatedAt)
			}
			return all[i].ID < all[j].ID
		})
		page.Total = len(all)
		start := min(offset, len(all))
		end := min(start+limit, len(all))
		page.Items = all[start:end]
		return nil
	})
	return page, err
}

// ResumeGroup 恢复尚未结束的组；已完成的组冲突拒绝。
func (s *Service) ResumeGroup(ctx context.Context, id string) (correlation.Group, error) {
	group, err := s.Group(ctx, id)
	if err != nil {
		return correlation.Group{}, err
	}
	queued := group.QueuedIndexes()
	if len(queued) == 0 {
		return correlation.Group{}, geology.Conflict("对比组已经结束")
	}
	s.runner.enqueue(id, queued)
	return group, nil
}

// process 处理组内一项：标记运行、计算、落定终态。任何单项失败都只
// 记录在该项上，不影响同组其他项。
func (r *GroupRunner) process(ctx context.Context, groupID string, index int) {
	defer r.release(groupID, index)
	var input correlation.Request
	var current correlation.Group
	marked := false
	err := r.service.repo.Update(ctx, func(state *persistence.State) (bool, error) {
		group, exists := state.Groups[groupID]
		if !exists || index < 0 || index >= len(group.Items) {
			return false, geology.Missing("对比组不存在")
		}
		if group.Items[index].Status != correlation.ItemQueued {
			return false, nil
		}
		input = group.ItemRequest(index)
		group.Items[index].Status = correlation.ItemRunning
		group.Status = group.DerivedStatus()
		group.UpdatedAt = time.Now().UTC()
		state.Groups[groupID] = group
		current = group.Clone()
		marked = true
		return true, nil
	})
	if err != nil {
		r.logger.Error("mark group item running failed", "group", groupID, "index", index, "error", err)
		return
	}
	if !marked {
		return // 已被其他处理接管或已经终态
	}

	result, reused, computeErr := r.service.compute(ctx, input)
	r.finish(groupID, index, current.UpdatedAt, result, reused, computeErr)
}

func (r *GroupRunner) finish(groupID string, index int, startedAt time.Time, result correlation.Result, reused bool, computeErr error) {
	item := func() correlation.GroupItem {
		if computeErr != nil {
			return correlation.GroupItem{Status: correlation.ItemFailed, Error: itemError(computeErr)}
		}
		return correlation.GroupItem{Status: correlation.ItemSucceeded, ComparisonID: result.ID, Reused: reused}
	}()
	err := r.service.repo.Update(context.Background(), func(state *persistence.State) (bool, error) {
		group, exists := state.Groups[groupID]
		if !exists || index < 0 || index >= len(group.Items) {
			return false, geology.Missing("对比组不存在")
		}
		if group.Items[index].Status != correlation.ItemRunning {
			return false, nil
		}
		updated := group.Items[index]
		updated.Status = item.Status
		updated.Reused = item.Reused
		updated.ComparisonID = item.ComparisonID
		updated.Error = item.Error
		group.Items[index] = updated
		group.Status = group.DerivedStatus()
		now := time.Now().UTC()
		if now.Before(startedAt) {
			now = startedAt
		}
		group.UpdatedAt = now
		state.Groups[groupID] = group
		return true, nil
	})
	if err != nil {
		// 持久化故障后健康检查转为不可用；遗留的运行状态会在重启时重置为排队。
		r.logger.Error("finish group item failed", "group", groupID, "index", index, "error", err)
	}
}

func itemError(err error) *correlation.ItemError {
	var problem *geology.Problem
	if errors.As(err, &problem) {
		return &correlation.ItemError{Code: problem.Code, Detail: problem.Detail}
	}
	return &correlation.ItemError{Code: "internal", Detail: "对比计算失败，请稍后重试"}
}
