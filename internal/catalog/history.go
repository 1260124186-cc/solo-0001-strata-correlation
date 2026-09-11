package catalog

import (
	"context"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/geology"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/persistence"
)

type HistoryPage struct {
	Items  []geology.Event `json:"items"`
	Total  int             `json:"total"`
	Offset int             `json:"offset"`
	Limit  int             `json:"limit"`
}

func (s *Service) Revision(ctx context.Context, id string, version int) (geology.Revision, error) {
	var result geology.Revision
	err := s.repo.View(ctx, func(state persistence.State) error {
		var err error
		result, err = state.Revision(id, version)
		return err
	})
	return result, err
}

// History 按真实版本号升序列出事件。branch 为空时返回全部分叉线的事件，
// 显式指定某条修订线时只返回该线（含其分叉事件）。
func (s *Service) History(ctx context.Context, id string, branch string, offset, limit int) (HistoryPage, error) {
	if branch == "" {
		// 不过滤：需要先确认剖面存在。
	} else {
		normalized, err := resolveBranch(branch)
		if err != nil {
			return HistoryPage{}, err
		}
		branch = normalized
	}
	if offset < 0 || offset > 1000000 || limit < 1 || limit > 100 {
		return HistoryPage{}, geology.Invalid("pagination", "分页参数超出范围")
	}
	result := HistoryPage{Items: []geology.Event{}, Offset: offset, Limit: limit}
	err := s.repo.View(ctx, func(state persistence.State) error {
		history, ok := state.Histories[id]
		if !ok {
			return geology.Missing("剖面不存在")
		}
		if branch != "" {
			if _, _, branchErr := state.LookupBranch(id, branch); branchErr != nil {
				return branchErr
			}
		}
		events := make([]geology.Event, 0, len(history))
		for _, r := range history {
			if branch == "" || r.Event.Branch == branch {
				events = append(events, r.Event)
			}
		}
		result.Total = len(events)
		start := min(offset, len(events))
		end := min(start+limit, len(events))
		result.Items = append(result.Items, events[start:end]...)
		return nil
	})
	return result, err
}
