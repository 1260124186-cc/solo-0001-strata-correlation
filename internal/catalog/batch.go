package catalog

import (
	"context"
	"errors"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/geology"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/persistence"
)

// MaxBatchItems 限制单次批量修订的项数，保证请求体与单次快照增长有界。
const MaxBatchItems = 100

// BatchItem 描述对一份剖面的一次修订，Action 取 metadata / layers / seal / reopen。
type BatchItem struct {
	ID              string            `json:"id"`
	Action          string            `json:"action"`
	ExpectedVersion int               `json:"expected_version"`
	Reason          string            `json:"reason"`
	Metadata        *geology.Metadata `json:"metadata"`
	Layers          []geology.Layer   `json:"layers"`
}

// BatchResult 记录批次中单项的结果；Error 非空表示该项未写入任何内容。
type BatchResult struct {
	ID      string           `json:"id"`
	Action  string           `json:"action"`
	OK      bool             `json:"ok"`
	Version int              `json:"version,omitempty"`
	State   geology.State    `json:"state,omitempty"`
	Error   *geology.Problem `json:"error,omitempty"`
}

// Batch 在一次快照写入中修订多份剖面。原子边界落在单项上：每项独立校验，
// 失败的项不修改对应剖面，成功的项合并为一次原子落盘。批次整体不是原子单位，
// 返回顺序与请求一致，逐项报告成功版本或失败原因。
func (s *Service) Batch(ctx context.Context, items []BatchItem) ([]BatchResult, error) {
	results := make([]BatchResult, len(items))
	err := s.repo.Update(ctx, func(state *persistence.State) (bool, error) {
		changed := false
		for i, item := range items {
			p, err := applyItem(state, item)
			results[i] = BatchResult{ID: item.ID, Action: item.Action, OK: err == nil}
			if err != nil {
				results[i].Error = problemOf(err)
				continue
			}
			results[i].Version = p.Version
			results[i].State = p.State
			changed = true
		}
		return changed, nil
	})
	if err != nil {
		return nil, err
	}
	return results, nil
}

// applyItem 按动作分派到与单份接口相同的校验和追加逻辑；同一剖面在批次中
// 出现多次时，后一项看到前一项产生的新版本。
func applyItem(state *persistence.State, item BatchItem) (geology.Profile, error) {
	if item.ExpectedVersion < 1 {
		return geology.Profile{}, geology.Invalid("expected_version", "必须为正整数")
	}
	switch item.Action {
	case "metadata":
		if item.Metadata == nil {
			return geology.Profile{}, geology.Invalid("metadata", "metadata 修订必须提供 metadata 对象")
		}
		if item.Layers != nil {
			return geology.Profile{}, geology.Invalid("layers", "metadata 修订不接受 layers")
		}
		return applyEdit(state, item.ID, EditMetadata{item.ExpectedVersion, *item.Metadata, item.Reason})
	case "layers":
		if item.Layers == nil {
			return geology.Profile{}, geology.Invalid("layers", "必须提供分层数组，清空时使用 []")
		}
		if item.Metadata != nil {
			return geology.Profile{}, geology.Invalid("metadata", "layers 修订不接受 metadata")
		}
		return applyReplace(state, item.ID, ReplaceLayers{item.ExpectedVersion, item.Layers, item.Reason})
	case "seal", "reopen":
		if item.Metadata != nil || item.Layers != nil {
			return geology.Profile{}, geology.Invalid("metadata", "状态修订不接受 metadata 或 layers")
		}
		target := geology.Sealed
		if item.Action == "reopen" {
			target = geology.Draft
		}
		return applyChange(state, item.ID, target, StateChange{item.ExpectedVersion, item.Reason})
	}
	return geology.Profile{}, geology.Invalid("action", "只支持 metadata / layers / seal / reopen")
}

func problemOf(err error) *geology.Problem {
	var problem *geology.Problem
	if errors.As(err, &problem) {
		return problem
	}
	return &geology.Problem{Code: "internal", Detail: "服务暂时无法完成请求"}
}
