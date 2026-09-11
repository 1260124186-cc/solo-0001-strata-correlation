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

func (s *Service) History(ctx context.Context, id string, f geology.HistoryFilter) (HistoryPage, error) {
	if err := f.Validate(); err != nil {
		return HistoryPage{}, err
	}
	result := HistoryPage{Items: []geology.Event{}, Offset: f.Offset, Limit: f.Limit}
	err := s.repo.View(ctx, func(state persistence.State) error {
		history, ok := state.Histories[id]
		if !ok {
			return geology.Missing("剖面不存在")
		}
		matched := geology.SelectEvents(history, f)
		result.Total = len(matched)
		start := min(f.Offset, len(matched))
		end := min(start+f.Limit, len(matched))
		result.Items = append(result.Items, matched[start:end]...)
		return nil
	})
	return result, err
}
