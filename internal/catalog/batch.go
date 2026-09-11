package catalog

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/geology"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/persistence"
)

const (
	maxBatchItems   = 200
	maxBatchRecords = 1000
)

// BatchItemInput 是批量修订中的一条。动作只能是
// metadata / layers / seal / reopen；载荷字段仅对应动作允许出现。
type BatchItemInput struct {
	ProfileID string `json:"profile_id"`
	Action    string `json:"action"`
	// Reason 缺省时使用请求级 BatchInput.Reason。
	Reason string `json:"reason"`

	ExpectedVersion int `json:"expected_version"`

	// Metadata 为指针以区分“未提供”和提供空对象。
	Metadata *geology.Metadata `json:"metadata,omitempty"`
	// Layers 为指针以区分“未提供”、null（拒绝）和 []（清空）。
	Layers *[]geology.Layer `json:"layers,omitempty"`
}

type BatchInput struct {
	Reason string           `json:"reason"`
	Items  []BatchItemInput `json:"items"`
}

type ItemOutcome struct {
	Index           int    `json:"index"`
	ProfileID       string `json:"profile_id"`
	Action          string `json:"action"`
	Status          string `json:"status"`
	Version         int    `json:"version,omitempty"`
	ErrorCode       string `json:"error_code,omitempty"`
	Error           string `json:"error,omitempty"`
	ExpectedVersion int    `json:"expected_version"`
}

type BatchView struct {
	ID        string        `json:"id"`
	Reason    string        `json:"reason"`
	Status    string        `json:"status"`
	Items     []ItemOutcome `json:"items"`
	CreatedAt time.Time     `json:"created_at"`
	UpdatedAt time.Time     `json:"updated_at"`
}

func newBatchID() (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return "bat_" + hex.EncodeToString(bytes), nil
}

// finalizeContext 与请求生命周期解耦：调用方断开连接不应阻止批次
// 终结落盘。终结只是一次本机快照写入，用短超时兜底。
func finalizeContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
}

// BatchRevise 在一次操作中顺序修订多份剖面。
//
// 原子边界落在单份剖面：每个条目与批次台账中该条目的结果在同一个
// 快照提交点落盘，因此任一条目要么完整写入（修订与 success 结果
// 同时可见），要么完全不写。业务错误（404/409/422 类）只让该条目
// 失败并继续执行后续条目；持久化故障会中断整批，未执行条目标记为
// not_attempted 并落盘。调用方始终拿到逐条目结果；进程崩溃后通过
// 启动恢复得到同样的事实，也可用 GetBatch 复查。
func (s *Service) BatchRevise(ctx context.Context, input BatchInput) (BatchView, error) {
	defaultReason := strings.TrimSpace(input.Reason)
	if input.Items == nil {
		return BatchView{}, geology.Invalid("items", "必须提供修订条目数组")
	}
	if len(input.Items) == 0 {
		return BatchView{}, geology.Invalid("items", "修订条目不能为空")
	}
	if len(input.Items) > maxBatchItems {
		return BatchView{}, geology.Invalid("items", "单次批量修订最多 200 条")
	}
	if defaultReason != "" {
		if err := geology.Text("reason", defaultReason, 1, 500); err != nil {
			return BatchView{}, err
		}
	}

	type prepared struct {
		item       persistence.BatchItem
		metadata   *geology.Metadata
		layers     *[]geology.Layer
		shapeError error
	}
	preparedItems := make([]prepared, len(input.Items))
	for i, in := range input.Items {
		reason := strings.TrimSpace(in.Reason)
		if reason == "" {
			reason = defaultReason
		}
		entry := persistence.BatchItem{
			ProfileID:       strings.TrimSpace(in.ProfileID),
			Action:          in.Action,
			Reason:          reason,
			ExpectedVersion: in.ExpectedVersion,
			Status:          persistence.ItemPending,
		}
		pre := prepared{item: entry, metadata: in.Metadata, layers: in.Layers}
		switch {
		case !geology.ValidID(strings.TrimSpace(in.ProfileID), "prf_"):
			pre.shapeError = geology.Invalid("profile_id", "剖面编号无效")
		case in.ExpectedVersion < 1:
			pre.shapeError = geology.Invalid("expected_version", "必须为正整数")
		case in.Action == "metadata":
			if in.Metadata == nil || in.Layers != nil {
				pre.shapeError = geology.Invalid("action", "metadata 修订必须且只能提供 metadata")
			}
		case in.Action == "layers":
			if in.Layers == nil || in.Metadata != nil {
				pre.shapeError = geology.Invalid("action", "layers 修订必须且只能提供 layers，清空时使用 []")
			}
		case in.Action == "seal", in.Action == "reopen":
			if in.Metadata != nil || in.Layers != nil {
				pre.shapeError = geology.Invalid("action", "状态变更不接受 metadata 或 layers")
			}
		default:
			pre.shapeError = geology.Invalid("action", "只支持 metadata、layers、seal、reopen")
		}
		if pre.shapeError == nil {
			pre.shapeError = geology.Text("reason", reason, 1, 500)
		}
		preparedItems[i] = pre
	}

	id, err := newBatchID()
	if err != nil {
		return BatchView{}, err
	}
	now := time.Now().UTC()
	items := make([]persistence.BatchItem, len(preparedItems))
	for i, pre := range preparedItems {
		items[i] = pre.item
	}
	if _, err = s.repo.Commit(ctx, func(state *persistence.State) (bool, error) {
		if len(state.Batches) >= maxBatchRecords {
			return false, geology.Conflict("最多保留 1000 个批量修订记录")
		}
		if _, exists := state.Batches[id]; exists {
			return false, geology.Conflict("批量修订编号重复，请重试")
		}
		state.Batches[id] = persistence.BatchRecord{
			ID:        id,
			Reason:    defaultReason,
			Status:    persistence.BatchRunning,
			Items:     items,
			CreatedAt: now,
			UpdatedAt: now,
		}
		return true, nil
	}); err != nil {
		return BatchView{}, err
	}

	// outcomes 只在 committed 后更新，因此它始终等于已落盘的台账事实。
	outcomes := make([]persistence.BatchItem, len(items))
	copy(outcomes, items)
	finished := 0
	lastTime := now
	var storageErr error

	for i, pre := range preparedItems {
		var entry persistence.BatchItem
		var entryTime time.Time
		committed, commitErr := s.repo.Commit(ctx, func(state *persistence.State) (bool, error) {
			batch := state.Batches[id]
			// 条目事件时间与台账更新时间共享同一个时钟采样，保证
			// 事件时间落在批次 [CreatedAt, UpdatedAt] 区间内。
			itemNow := nextTime(batch.UpdatedAt)
			var problem *geology.Problem
			if pre.shapeError != nil {
				if !errors.As(pre.shapeError, &problem) {
					return false, pre.shapeError
				}
			}
			resultVersion := 0
			if problem == nil {
				var p geology.Profile
				var applyErr error
				switch pre.item.Action {
				case "metadata":
					metadata, normErr := geology.NormalizeMetadata(*pre.metadata)
					if normErr != nil {
						applyErr = normErr
					} else {
						p, applyErr = applyMetadata(state, pre.item.ProfileID, pre.item.ExpectedVersion, metadata, pre.item.Reason, itemNow)
					}
				case "layers":
					p, applyErr = applyLayers(state, pre.item.ProfileID, pre.item.ExpectedVersion, *pre.layers, pre.item.Reason, itemNow)
				case "seal":
					p, applyErr = applyStateChange(state, pre.item.ProfileID, geology.Sealed, pre.item.ExpectedVersion, pre.item.Reason, itemNow)
				case "reopen":
					p, applyErr = applyStateChange(state, pre.item.ProfileID, geology.Draft, pre.item.ExpectedVersion, pre.item.Reason, itemNow)
				}
				if applyErr != nil {
					if errors.As(applyErr, &problem) {
						applyErr = nil
					} else {
						return false, applyErr
					}
				}
				if applyErr == nil {
					resultVersion = p.Version
				}
			}
			e := batch.Items[i]
			if problem != nil {
				e.Status = persistence.ItemFailed
				e.ErrorCode = problem.Code
				e.Error = problem.Detail
			} else {
				e.Status = persistence.ItemSuccess
				e.ResultVersion = resultVersion
			}
			batch.Items[i] = e
			batch.UpdatedAt = itemNow
			state.Batches[id] = batch
			entry = e
			entryTime = itemNow
			return true, nil
		})
		if committed {
			// 即便 commitErr 非空（重命名已完成、仅目录同步失败），
			// 修订与条目结果也已经落盘，必须按已提交记账。
			outcomes[i] = entry
			lastTime = entryTime
			finished++
		}
		if commitErr != nil {
			storageErr = commitErr
			break
		}
	}

	var status string
	var updatedAtTime time.Time
	if storageErr == nil {
		status, updatedAtTime = s.finalizeCompleted(ctx, id, lastTime)
	} else {
		// 当前条目（未提交时）及后续条目都没有修订落盘，统一标记
		// not_attempted；已提交的条目保持 success/failed 不动。
		for i := finished; i < len(outcomes); i++ {
			outcomes[i].Status = persistence.ItemNotAttempted
			outcomes[i].ErrorCode = "interrupted"
			outcomes[i].Error = "批量修订因存储中断未执行该条目"
		}
		status, updatedAtTime = s.finalizeInterrupted(ctx, id, finished, lastTime, len(outcomes))
	}

	view := buildBatchView(id, defaultReason, now, updatedAtTime, status, outcomes)
	return view, nil
}

// finalizeCompleted 写入批次完成标记。所有条目结果此前均已逐条落盘，
// 因此即使该标记提交失败，启动恢复也会因没有 pending 条目而把批次
// 终结为 completed——返回 completed 与磁盘的最终事实一致。
func (s *Service) finalizeCompleted(ctx context.Context, id string, lastTime time.Time) (string, time.Time) {
	finalCtx, cancel := finalizeContext(ctx)
	defer cancel()
	var markedAt time.Time
	_, _ = s.repo.Commit(finalCtx, func(state *persistence.State) (bool, error) {
		batch := state.Batches[id]
		batch.Status = persistence.BatchCompleted
		batch.UpdatedAt = nextTime(batch.UpdatedAt)
		state.Batches[id] = batch
		markedAt = batch.UpdatedAt
		return true, nil
	})
	if markedAt.IsZero() {
		markedAt = lastTime
	}
	return persistence.BatchCompleted, markedAt
}

// finalizeInterrupted 尽力把批次在磁盘上终结为 interrupted。终结提交
// 未成功时（仓库已故障等），启动恢复会把遗留的 pending 条目转换成
// 完全相同的 interrupted/not_attempted 事实。
func (s *Service) finalizeInterrupted(ctx context.Context, id string, finished int, lastTime time.Time, total int) (string, time.Time) {
	finalCtx, cancel := finalizeContext(ctx)
	defer cancel()
	var markedAt time.Time
	_, _ = s.repo.Commit(finalCtx, func(state *persistence.State) (bool, error) {
		batch := state.Batches[id]
		for i := finished; i < total; i++ {
			if batch.Items[i].Status != persistence.ItemPending {
				continue
			}
			batch.Items[i].Status = persistence.ItemNotAttempted
			batch.Items[i].ErrorCode = "interrupted"
			batch.Items[i].Error = "批量修订因存储中断未执行该条目"
		}
		batch.Status = persistence.BatchInterrupted
		batch.UpdatedAt = nextTime(batch.UpdatedAt)
		state.Batches[id] = batch
		markedAt = batch.UpdatedAt
		return true, nil
	})
	if markedAt.IsZero() {
		markedAt = lastTime
	}
	return persistence.BatchInterrupted, markedAt
}

func buildBatchView(id, reason string, createdAt, updatedAt time.Time, status string, items []persistence.BatchItem) BatchView {
	view := BatchView{
		ID:        id,
		Reason:    reason,
		Status:    status,
		Items:     make([]ItemOutcome, 0, len(items)),
		CreatedAt: createdAt,
		UpdatedAt: updatedAt,
	}
	for i, item := range items {
		view.Items = append(view.Items, ItemOutcome{
			Index:           i,
			ProfileID:       item.ProfileID,
			Action:          item.Action,
			Status:          item.Status,
			Version:         item.ResultVersion,
			ErrorCode:       item.ErrorCode,
			Error:           item.Error,
			ExpectedVersion: item.ExpectedVersion,
		})
	}
	return view
}

// GetBatch 返回持久化的批次台账。崩溃后 running 批次已在启动恢复时
// 终结，返回值中不会出现 pending 条目。
func (s *Service) GetBatch(ctx context.Context, id string) (BatchView, error) {
	var view BatchView
	err := s.repo.View(ctx, func(state persistence.State) error {
		record, ok := state.Batches[id]
		if !ok {
			return geology.Missing("批量修订不存在")
		}
		view = recordView(record)
		return nil
	})
	return view, err
}

func recordView(record persistence.BatchRecord) BatchView {
	return buildBatchView(record.ID, record.Reason, record.CreatedAt, record.UpdatedAt, record.Status, record.Items)
}
