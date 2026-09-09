package catalog

import (
	"context"
	"github.com/1260124186-cc/solo-0003-strata-correlation/internal/geology"
	"github.com/1260124186-cc/solo-0003-strata-correlation/internal/persistence"
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

func (s *Service) History(ctx context.Context, id string, offset, limit int) (HistoryPage, error) {
	if offset < 0 || offset > 1000000 || limit < 1 || limit > 100 {
		return HistoryPage{}, geology.Invalid("pagination", "分页参数超出范围")
	}
	result := HistoryPage{Items: []geology.Event{}, Offset: offset, Limit: limit}
	err := s.repo.View(ctx, func(state persistence.State) error {
		history, ok := state.Histories[id]
		if !ok {
			return geology.Missing("剖面不存在")
		}
		result.Total = len(history)
		start := min(offset, len(history))
		end := min(start+limit, len(history))
		for i := start; i < end; i++ {
			result.Items = append(result.Items, history[i].Event)
		}
		return nil
	})
	return result, err
}
