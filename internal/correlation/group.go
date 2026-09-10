package correlation

import (
	"fmt"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/geology"
	"time"
)

// 成组对比以一份锁定参考版本为中心，批量对比多个目标版本；每个目标
// 携带独立偏移并拥有独立处理状态，单项失败不影响其余项。

const (
	ItemQueued    = "queued"
	ItemRunning   = "running"
	ItemSucceeded = "succeeded"
	ItemFailed    = "failed"

	GroupQueued    = "queued"
	GroupRunning   = "running"
	GroupCompleted = "completed"
)

const MaxGroupTargets = 100

// GroupTarget 是组内一个目标版本及其相对参考坐标的右侧偏移。
type GroupTarget struct {
	Target   Reference `json:"target"`
	OffsetMM int64     `json:"offset_mm"`
}

// GroupRequest 一次提交一份参考版本和多个目标版本。
type GroupRequest struct {
	Reference Reference     `json:"reference"`
	Targets   []GroupTarget `json:"targets"`
}

type ItemError struct {
	Code   string `json:"code"`
	Detail string `json:"detail"`
}

type GroupItem struct {
	Index        int        `json:"index"`
	Target       Reference  `json:"target"`
	OffsetMM     int64      `json:"offset_mm"`
	Status       string     `json:"status"`
	Reused       bool       `json:"reused"`
	ComparisonID string     `json:"comparison_id,omitempty"`
	Error        *ItemError `json:"error,omitempty"`
}

type Group struct {
	ID        string      `json:"id"`
	Reference Reference   `json:"reference"`
	Status    string      `json:"status"`
	Items     []GroupItem `json:"items"`
	CreatedAt time.Time   `json:"created_at"`
	UpdatedAt time.Time   `json:"updated_at"`
}

func (r GroupRequest) Validate() error {
	if !geology.ValidID(r.Reference.ID, "prf_") {
		return geology.Invalid("reference", "参考剖面编号无效")
	}
	if r.Reference.Version < 1 {
		return geology.Invalid("reference", "需要指定正整数历史版本")
	}
	if len(r.Targets) == 0 {
		return geology.Invalid("targets", "至少需要一个目标版本")
	}
	if len(r.Targets) > MaxGroupTargets {
		return geology.Invalid("targets", fmt.Sprintf("每组最多 %d 个目标版本", MaxGroupTargets))
	}
	seen := make(map[string]bool, len(r.Targets))
	for i, target := range r.Targets {
		field := fmt.Sprintf("targets[%d]", i)
		if !geology.ValidID(target.Target.ID, "prf_") {
			return geology.Invalid(field+".target", "目标剖面编号无效")
		}
		if target.Target.Version < 1 {
			return geology.Invalid(field+".target", "需要指定正整数历史版本")
		}
		if target.OffsetMM < -geology.MaxDepth || target.OffsetMM > geology.MaxDepth {
			return geology.Invalid(field+".offset_mm", "偏移超出一千米范围")
		}
		if target.Target == r.Reference {
			return geology.Invalid(field+".target", "目标不能与参考版本相同")
		}
		key := fmt.Sprintf("%s@%d#%d", target.Target.ID, target.Target.Version, target.OffsetMM)
		if seen[key] {
			return geology.Invalid(field, "目标版本与偏移重复")
		}
		seen[key] = true
	}
	return nil
}

func (i GroupItem) Terminal() bool { return i.Status == ItemSucceeded || i.Status == ItemFailed }

// DerivedStatus 根据各项状态推导对比组状态：全部终态才完成，
// 只要存在运行中或终态项即为运行中，否则排队中。
func (g Group) DerivedStatus() string {
	started := false
	for _, item := range g.Items {
		if item.Status == ItemRunning || item.Terminal() {
			started = true
		}
		if !item.Terminal() {
			if item.Status == ItemRunning {
				return GroupRunning
			}
		}
	}
	if !started {
		return GroupQueued
	}
	for _, item := range g.Items {
		if !item.Terminal() {
			return GroupRunning
		}
	}
	return GroupCompleted
}

func (g Group) QueuedIndexes() []int {
	out := make([]int, 0, len(g.Items))
	for i := range g.Items {
		if g.Items[i].Status == ItemQueued {
			out = append(out, i)
		}
	}
	return out
}

func (g Group) ItemRequest(i int) Request {
	item := g.Items[i]
	return Request{Left: g.Reference, Right: item.Target, OffsetMM: item.OffsetMM}
}

func (g Group) Clone() Group {
	out := g
	out.Items = make([]GroupItem, len(g.Items))
	for i, item := range g.Items {
		if item.Error != nil {
			failure := *item.Error
			item.Error = &failure
		}
		out.Items[i] = item
	}
	return out
}
