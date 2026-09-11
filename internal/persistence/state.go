package persistence

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/correlation"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/geology"
	"reflect"
)

// 批量修订任务状态。
const (
	BatchRunning     = "running"
	BatchCompleted   = "completed"
	BatchInterrupted = "interrupted"
)

// 单条目处理结果。
const (
	ItemSuccess      = "success"
	ItemFailed       = "failed"
	ItemNotAttempted = "not_attempted"
	ItemPending      = "pending"
)

type State struct {
	Schema      int                           `json:"schema"`
	Histories   map[string][]geology.Revision `json:"histories"`
	Comparisons map[string]correlation.Result `json:"comparisons"`
	Batches     map[string]BatchRecord        `json:"batches"`
}

// BatchItem 是批次中的一个修订意图及其最终结果。它与对应剖面修订
// 在同一个快照提交点落盘：成功时历史版本与 success 结果同时可见。
type BatchItem struct {
	ProfileID string `json:"profile_id"`
	Action    string `json:"action"`
	Reason    string `json:"reason"`

	ExpectedVersion int `json:"expected_version"`

	Status string `json:"status"`
	// success：提交后的版本号；其余状态为 0。
	ResultVersion int `json:"result_version,omitempty"`
	// failed：业务校验冲突原因；not_attempted：未执行原因。
	ErrorCode string `json:"error_code,omitempty"`
	Error     string `json:"error,omitempty"`
}

// BatchRecord 是批量修订的持久化台账。批次本身不是原子单元，但
// 每个条目与它引用的剖面修订共享一个原子提交点，所以台账始终能
// 解释磁盘上已经发生了什么。running 状态只会在进程存活期间出现，
// 打开数据目录时会被恢复为 completed 或 interrupted。
type BatchRecord struct {
	ID        string      `json:"id"`
	Reason    string      `json:"reason,omitempty"`
	Status    string      `json:"status"`
	Items     []BatchItem `json:"items"`
	CreatedAt time.Time   `json:"created_at"`
	UpdatedAt time.Time   `json:"updated_at"`
}

func emptyState() State {
	return State{
		Schema:      1,
		Histories:   map[string][]geology.Revision{},
		Comparisons: map[string]correlation.Result{},
		Batches:     map[string]BatchRecord{},
	}
}

func (s State) Clone() State {
	out := emptyState()
	for id, revisions := range s.Histories {
		copies := make([]geology.Revision, len(revisions))
		for i, r := range revisions {
			copies[i] = r.Clone()
		}
		out.Histories[id] = copies
	}
	for id, result := range s.Comparisons {
		out.Comparisons[id] = result.Clone()
	}
	for id, batch := range s.Batches {
		items := make([]BatchItem, len(batch.Items))
		copy(items, batch.Items)
		batch.Items = items
		out.Batches[id] = batch
	}
	return out
}

func (s State) Latest(id string) (geology.Profile, error) {
	history, ok := s.Histories[id]
	if !ok || len(history) == 0 {
		return geology.Profile{}, geology.Missing("剖面不存在")
	}
	return history[len(history)-1].Profile.Clone(), nil
}

func (s State) Revision(id string, version int) (geology.Revision, error) {
	history, ok := s.Histories[id]
	if !ok {
		return geology.Revision{}, geology.Missing("剖面不存在")
	}
	if version < 1 || version > len(history) {
		return geology.Revision{}, geology.Missing("历史版本不存在")
	}
	return history[version-1].Clone(), nil
}

func (s State) Validate() error {
	if s.Schema != 1 || s.Histories == nil || s.Comparisons == nil || s.Batches == nil {
		return fmt.Errorf("unsupported snapshot shape")
	}
	for id, history := range s.Histories {
		if len(history) == 0 {
			return fmt.Errorf("empty history %s", id)
		}
		for i, r := range history {
			if r.Profile.ID != id || r.Profile.Version != i+1 || r.Event.Version != i+1 || !r.Event.At.Equal(r.Profile.UpdatedAt) {
				return fmt.Errorf("inconsistent revision %s/%d", id, i+1)
			}
			if err := r.Profile.Validate(); err != nil {
				return fmt.Errorf("invalid revision %s: %w", id, err)
			}
			if err := geology.Text("reason", r.Event.Reason, 1, 500); err != nil {
				return err
			}
			if i == 0 {
				if r.Event.Action != "create" || r.Profile.State != geology.Draft {
					return fmt.Errorf("invalid initial revision")
				}
			} else {
				before := history[i-1].Profile
				if !before.CreatedAt.Equal(r.Profile.CreatedAt) || r.Profile.UpdatedAt.Before(before.UpdatedAt) {
					return fmt.Errorf("invalid revision chronology")
				}
				if err := validateStep(before, r); err != nil {
					return err
				}
			}
		}
	}
	for id, result := range s.Comparisons {
		if id != result.ID || id != result.Request.Key() || result.Algorithm != correlation.Algorithm || result.CreatedAt.IsZero() {
			return fmt.Errorf("invalid comparison identity")
		}
		a, err := s.Revision(result.Request.Left.ID, result.Request.Left.Version)
		if err != nil {
			return err
		}
		b, err := s.Revision(result.Request.Right.ID, result.Request.Right.Version)
		if err != nil {
			return err
		}
		computed, err := correlation.Align(a.Profile, b.Profile, result.Request, result.CreatedAt)
		if err != nil {
			return err
		}
		expected, _ := json.Marshal(computed)
		actual, _ := json.Marshal(result)
		if string(expected) != string(actual) {
			return fmt.Errorf("comparison data mismatch")
		}
	}
	for id, batch := range s.Batches {
		if err := batch.validate(id, s); err != nil {
			return err
		}
	}
	return nil
}

func validateStep(before geology.Profile, r geology.Revision) error {
	after := r.Profile
	switch r.Event.Action {
	case "metadata", "layers":
		if before.State != geology.Draft || after.State != geology.Draft {
			return fmt.Errorf("edited sealed revision")
		}
		if r.Event.Action == "layers" && before.Metadata != after.Metadata {
			return fmt.Errorf("layers edit changed metadata")
		}
		if r.Event.Action == "metadata" && !reflect.DeepEqual(before.Layers, after.Layers) {
			return fmt.Errorf("metadata edit changed layers")
		}
	case "seal", "reopen":
		expected := geology.Sealed
		if r.Event.Action == "reopen" {
			expected = geology.Draft
		}
		if after.State != expected || before.State == expected || before.Metadata != after.Metadata || !reflect.DeepEqual(before.Layers, after.Layers) {
			return fmt.Errorf("invalid state change")
		}
	default:
		return fmt.Errorf("unknown revision action")
	}
	return nil
}

func (b BatchRecord) validate(id string, s State) error {
	if b.ID != id || !geology.ValidID(id, "bat_") || len(b.Items) == 0 || b.CreatedAt.IsZero() {
		return fmt.Errorf("invalid batch identity %s", id)
	}
	if err := geology.Text("reason", b.Reason, 0, 500); err != nil {
		return err
	}
	if b.UpdatedAt.Before(b.CreatedAt) {
		return fmt.Errorf("invalid batch chronology %s", id)
	}
	pending := 0
	notAttempted := 0
	for i, item := range b.Items {
		if err := item.validate(); err != nil {
			return fmt.Errorf("invalid batch %s item %d: %w", id, i, err)
		}
		switch item.Status {
		case ItemPending:
			pending++
		case ItemSuccess:
			history := s.Histories[item.ProfileID]
			if item.ResultVersion != item.ExpectedVersion+1 || item.ResultVersion > len(history) {
				return fmt.Errorf("batch %s item %d references missing revision", id, i)
			}
			revision := history[item.ResultVersion-1]
			if revision.Event.Action != item.Action || revision.Event.Version != item.ResultVersion ||
				revision.Event.Reason != item.Reason ||
				revision.Event.At.Before(b.CreatedAt) || revision.Event.At.After(b.UpdatedAt) {
				return fmt.Errorf("batch %s item %d does not match its committed revision", id, i)
			}
		case ItemNotAttempted:
			notAttempted++
		}
	}
	switch b.Status {
	case BatchRunning:
		// 运行中的批次只允许存在于存活进程；落盘快照出现该状态意味着
		// 上次在条目提交之间（或最后条目与完成标记之间）崩溃。允许
		// pending 为零：那种形状由恢复流程终结为 completed。
	case BatchCompleted:
		if pending != 0 || notAttempted != 0 {
			return fmt.Errorf("completed batch %s has unfinished items", id)
		}
	case BatchInterrupted:
		if pending != 0 || notAttempted == 0 {
			return fmt.Errorf("interrupted batch %s has ambiguous items", id)
		}
	default:
		return fmt.Errorf("unknown batch status %s", b.Status)
	}
	return nil
}

func (it BatchItem) validate() error {
	// 仅成功条目参与剖面一致性检查，其形状由关联的历史修订保证。
	// failed / pending / not_attempted 条目原样保留用户请求（可能
	// 包含空编号、版本 0、空动作或空理由），不能因输入本身非法而让
	// 整张台账在下次启动时不可读；这些状态只校验各自的结果一致性。
	if it.Status == ItemSuccess {
		if !geology.ValidID(it.ProfileID, "prf_") {
			return fmt.Errorf("invalid item target")
		}
		if it.ExpectedVersion < 1 {
			return fmt.Errorf("invalid expected version")
		}
		switch it.Action {
		case "metadata", "layers", "seal", "reopen":
		default:
			return fmt.Errorf("unknown item action")
		}
		if err := geology.Text("reason", it.Reason, 1, 500); err != nil {
			return err
		}
	}
	switch it.Status {
	case ItemSuccess:
		if it.ResultVersion < 1 || it.Error != "" || it.ErrorCode != "" {
			return fmt.Errorf("inconsistent success item")
		}
	case ItemFailed:
		if it.ResultVersion != 0 || it.Error == "" || it.ErrorCode == "" {
			return fmt.Errorf("inconsistent failed item")
		}
	case ItemNotAttempted:
		if it.ResultVersion != 0 || it.Error == "" || it.ErrorCode == "" {
			return fmt.Errorf("inconsistent not_attempted item")
		}
	case ItemPending:
		// pending 条目只允许出现在 running 批次中，即崩溃现场；批次级
		// 校验负责确认 completed/interrupted 不残留 pending。
		if it.ResultVersion != 0 || it.Error != "" || it.ErrorCode != "" {
			return fmt.Errorf("inconsistent pending item")
		}
	default:
		return fmt.Errorf("unknown item status")
	}
	return nil
}
