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

// MaxRevisionsPerProfile 是单个剖面自身的历史版本数上限。
const MaxRevisionsPerProfile = 500

func newID(prefix string) (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(bytes), nil
}

func nextTime(previous time.Time) time.Time {
	now := time.Now().UTC()
	if now.Before(previous) {
		return previous
	}
	return now
}

// CreateProfile 是建剖面请求：元数据之外携带研究区归属。
type CreateProfile struct {
	geology.Metadata
	AreaID string `json:"area_id"`
}

func (s *Service) Create(ctx context.Context, input CreateProfile) (geology.Profile, error) {
	normalized, err := geology.NormalizeMetadata(input.Metadata)
	if err != nil {
		return geology.Profile{}, err
	}
	areaID := strings.TrimSpace(input.AreaID)
	if areaID == "" {
		areaID = geology.DefaultAreaID
	} else if !geology.ValidAreaID(areaID) {
		return geology.Profile{}, geology.Invalid("area_id", "研究区编号无效")
	}
	id, err := newID("prf_")
	if err != nil {
		return geology.Profile{}, err
	}
	now := time.Now().UTC()
	p := geology.Profile{ID: id, Metadata: normalized, Layers: []geology.Layer{}, State: geology.Draft, Version: 1, CreatedAt: now, UpdatedAt: now}
	err = s.repo.Update(ctx, func(state *persistence.State) (bool, error) {
		// 研究区配额与整库上限在同一个持锁事务内针对同一状态判定。
		if err := state.CheckCreateQuota(areaID); err != nil {
			return false, err
		}
		if _, exists := state.Histories[id]; exists {
			return false, geology.Conflict("剖面编号重复，请重试")
		}
		state.Memberships[id] = areaID
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
		if f.Area != "" {
			if !geology.ValidAreaID(f.Area) {
				return geology.Invalid("area", "研究区编号无效")
			}
			if _, ok := state.Areas[f.Area]; !ok {
				return geology.Invalid("area", "研究区不存在")
			}
		}
		profiles := make([]geology.Profile, 0, len(state.Histories))
		for id, history := range state.Histories {
			if f.Area != "" && state.Memberships[id] != f.Area {
				continue
			}
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
	profileID := revision.Profile.ID
	history := state.Histories[profileID]
	if len(history) >= MaxRevisionsPerProfile {
		return geology.Conflict("单个剖面最多保留 500 个版本")
	}
	if revision.Profile.Version != len(history)+1 {
		return geology.Conflict("版本顺序不一致")
	}
	if err := revision.Profile.Validate(); err != nil {
		return err
	}
	// 研究区版本配额在同一事务内检查，和研究区剖面配额使用同一套已提交数据。
	if err := state.CheckRevisionQuota(profileID); err != nil {
		return err
	}
	state.Histories[profileID] = append(history, revision.Clone())
	return nil
}

func (s *Service) Health() error { return s.repo.Health() }
