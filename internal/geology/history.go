package geology

import "time"

// 修订事件的操作类型，写入事件和历史过滤共用同一组常量。
const (
	ActionCreate   = "create"
	ActionMetadata = "metadata"
	ActionLayers   = "layers"
	ActionSeal     = "seal"
	ActionReopen   = "reopen"
)

var eventActions = []string{ActionCreate, ActionMetadata, ActionLayers, ActionSeal, ActionReopen}

func validAction(action string) bool {
	for _, a := range eventActions {
		if a == action {
			return true
		}
	}
	return false
}

// HistoryFilter 描述历史事件查询的过滤与分页条件。时间和版本边界均为闭区间，
// 零值表示该方向不设限。过滤先于分页生效，事件版本保持原始修订号。
type HistoryFilter struct {
	Action      string
	Since       time.Time
	Until       time.Time
	FromVersion int
	ToVersion   int
	Offset      int
	Limit       int
}

func (f HistoryFilter) Validate() error {
	if f.Action != "" && !validAction(f.Action) {
		return Invalid("action", "不支持的操作类型")
	}
	if !f.Since.IsZero() && !f.Until.IsZero() && f.Since.After(f.Until) {
		return Invalid("since", "时间范围起点不能晚于终点")
	}
	if f.FromVersion < 0 || f.FromVersion > 500 || f.ToVersion < 0 || f.ToVersion > 500 {
		return Invalid("version", "版本范围必须为 1 到 500")
	}
	if f.FromVersion > 0 && f.ToVersion > 0 && f.FromVersion > f.ToVersion {
		return Invalid("from_version", "版本范围起点不能大于终点")
	}
	if f.Offset < 0 || f.Offset > 1000000 {
		return Invalid("offset", "偏移量必须为 0 到 1000000")
	}
	if f.Limit < 1 || f.Limit > 100 {
		return Invalid("limit", "每页条数必须为 1 到 100")
	}
	return nil
}

// SelectEvents 按版本升序返回满足条件的事件。历史只能追加，过滤结果保持存储顺序；
// 新修订只追加在末尾，因此按偏移连续翻页不会重复或遗漏已有事件。
func SelectEvents(history []Revision, f HistoryFilter) []Event {
	matched := make([]Event, 0, len(history))
	for _, r := range history {
		e := r.Event
		if f.Action != "" && e.Action != f.Action {
			continue
		}
		if f.FromVersion > 0 && e.Version < f.FromVersion {
			continue
		}
		if f.ToVersion > 0 && e.Version > f.ToVersion {
			continue
		}
		if !f.Since.IsZero() && e.At.Before(f.Since) {
			continue
		}
		if !f.Until.IsZero() && e.At.After(f.Until) {
			continue
		}
		matched = append(matched, e)
	}
	return matched
}
