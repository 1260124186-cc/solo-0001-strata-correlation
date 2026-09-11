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

// resolveBranch 把空字符串归一到主线，并拒绝非法修订线标识。
func resolveBranch(branch string) (string, error) {
	branch = strings.TrimSpace(branch)
	if branch == "" {
		return geology.MainBranch, nil
	}
	if !geology.ValidBranch(branch) {
		return "", geology.Invalid("branch", "修订线标识无效")
	}
	return branch, nil
}

func (s *Service) Create(ctx context.Context, metadata geology.Metadata) (geology.Profile, error) {
	normalized, err := geology.NormalizeMetadata(metadata)
	if err != nil {
		return geology.Profile{}, err
	}
	id, err := newID("prf_")
	if err != nil {
		return geology.Profile{}, err
	}
	now := time.Now().UTC()
	p := geology.Profile{
		ID: id, Metadata: normalized, Layers: []geology.Layer{},
		State: geology.Draft, Version: 1, Branch: geology.MainBranch,
		CreatedAt: now, UpdatedAt: now,
	}
	err = s.repo.Update(ctx, func(state *persistence.State) (bool, error) {
		if len(state.Histories) >= 2000 {
			return false, geology.Conflict("最多保存 2000 个剖面")
		}
		if _, exists := state.Histories[id]; exists {
			return false, geology.Conflict("剖面编号重复，请重试")
		}
		state.Histories[id] = []geology.Revision{{
			Profile: p,
			Event:   geology.Event{Action: "create", Reason: "新建剖面", Version: 1, At: now, Branch: geology.MainBranch},
		}}
		state.Branches[id] = []persistence.Branch{{ID: geology.MainBranch, HeadVersion: 1}}
		return true, nil
	})
	return p, err
}

// Get 读取主修订线当前剖面。
func (s *Service) Get(ctx context.Context, id string) (geology.Profile, error) {
	return s.Head(ctx, id, geology.MainBranch)
}

// Head 读取指定修订线的当前剖面；branch 为空表示主线。
func (s *Service) Head(ctx context.Context, id, branch string) (geology.Profile, error) {
	branch, err := resolveBranch(branch)
	if err != nil {
		return geology.Profile{}, err
	}
	var result geology.Profile
	err = s.repo.View(ctx, func(state persistence.State) error {
		var viewErr error
		result, viewErr = state.Head(id, branch)
		return viewErr
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
		for id := range state.Histories {
			p, err := state.Head(id, geology.MainBranch)
			if err != nil {
				return err
			}
			profiles = append(profiles, p)
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

// nextVersion 按剖面内已使用的真实最大版本号分配新版本，绝不使用切片位置。
func nextVersion(history []geology.Revision) (int, error) {
	if len(history) >= 500 {
		return 0, geology.Conflict("单个剖面最多保留 500 个版本")
	}
	maxVersion := 0
	for _, r := range history {
		if r.Event.Version > maxVersion {
			maxVersion = r.Event.Version
		}
	}
	return maxVersion + 1, nil
}

// commitRevision 在指定修订线上追加新版本并推进该线头；版本与归属事件由调用方填好。
func commitRevision(state *persistence.State, revision geology.Revision, branch string) error {
	id := revision.Profile.ID
	history := state.Histories[id]
	if _, err := nextVersion(history); err != nil {
		return err
	}
	revision.Event.Branch = branch
	revision.Profile.Branch = branch
	if err := revision.Profile.Validate(); err != nil {
		return err
	}
	state.Histories[id] = append(history, revision.Clone())
	branches := state.Branches[id]
	for i := range branches {
		if branches[i].ID == branch {
			branches[i].HeadVersion = revision.Event.Version
			state.Branches[id] = branches
			return nil
		}
	}
	return geology.Missing("修订线不存在")
}

func (s *Service) Health() error { return s.repo.Health() }
