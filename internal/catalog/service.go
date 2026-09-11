package catalog

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/geology"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/persistence"
	"strings"
	"time"
)

type Service struct{ repo *persistence.Repository }

func New(repo *persistence.Repository) *Service { return &Service{repo: repo} }

func newID() (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return "prf_" + hex.EncodeToString(bytes), nil
}

func nextTime(previous time.Time) time.Time {
	now := time.Now().UTC()
	if now.Before(previous) {
		return previous
	}
	return now
}

func (s *Service) Create(ctx context.Context, metadata geology.Metadata) (geology.Profile, error) {
	normalized, err := geology.NormalizeMetadata(metadata)
	if err != nil {
		return geology.Profile{}, err
	}
	id, err := newID()
	if err != nil {
		return geology.Profile{}, err
	}
	now := time.Now().UTC()
	p := geology.Profile{ID: id, Metadata: normalized, Layers: []geology.Layer{}, State: geology.Draft, Version: 1, CreatedAt: now, UpdatedAt: now}
	err = s.repo.Update(ctx, func(state *persistence.State) (bool, error) {
		if len(state.Histories) >= 2000 {
			return false, geology.Conflict("最多保存 2000 个剖面")
		}
		if _, exists := state.Histories[id]; exists {
			return false, geology.Conflict("剖面编号重复，请重试")
		}
		state.Histories[id] = []geology.Revision{{Profile: p, Event: geology.Event{Action: "create", Reason: "新建剖面", Version: 1, At: now}}}
		return true, nil
	})
	return p, err
}

func (s *Service) Get(ctx context.Context, id string) (geology.Profile, error) {
	var result geology.Profile
	err := s.repo.View(ctx, func(state persistence.State) error {
		var err error
		result, err = state.Latest(id)
		return err
	})
	return result, err
}

func (s *Service) List(ctx context.Context, f geology.Filter) (geology.Page, error) {
	if err := f.Validate(); err != nil {
		return geology.Page{}, err
	}
	var result geology.Page
	err := s.repo.View(ctx, func(state persistence.State) error {
		profiles := make([]geology.Profile, 0, len(state.Histories))
		for _, history := range state.Histories {
			profiles = append(profiles, history[len(history)-1].Profile)
		}
		result = geology.Select(profiles, f)
		return nil
	})
	return result, err
}

func normalizedReason(reason string) (string, error) {
	reason = strings.TrimSpace(reason)
	return reason, geology.Text("reason", reason, 1, 500)
}

func appendRevision(state *persistence.State, revision geology.Revision) error {
	history := state.Histories[revision.Profile.ID]
	if len(history) >= 500 {
		return geology.Conflict("单个剖面最多保留 500 个未归档版本，请先归档更早的版本")
	}
	// Version numbers continue after the archived prefix; archiving never
	// renumbers existing revisions.
	if revision.Profile.Version != len(state.Archived[revision.Profile.ID])+len(history)+1 {
		return geology.Conflict("版本顺序不一致")
	}
	if err := revision.Profile.Validate(); err != nil {
		return err
	}
	state.Histories[revision.Profile.ID] = append(history, revision.Clone())
	return nil
}

func (s *Service) Health() error { return s.repo.Health() }
