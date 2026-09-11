package catalog

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/geology"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/persistence"
)

// ForkRequest 固定从 source_version 分叉出新的并行修订线。
type ForkRequest struct {
	SourceVersion int    `json:"source_version"`
	Name          string `json:"name"`
	Reason        string `json:"reason"`
}

// BranchView 是分叉线与主线关系的对外描述。
type BranchView struct {
	ID          string    `json:"id"`
	Name        string    `json:"name,omitempty"`
	ForkPoint   int       `json:"fork_point"`
	HeadVersion int       `json:"head_version"`
	IsMain      bool      `json:"is_main"`
	CreatedAt   time.Time `json:"created_at"`
	// 关系字段（仅关系查询时填充）
	MergeBase    int  `json:"merge_base,omitempty"`
	Ahead        int  `json:"ahead"`
	Behind       int  `json:"behind"`
	Merged       bool `json:"merged"`
	SourceSealed bool `json:"source_sealed"`
}

// ForkResult 返回分叉产生的头版本、事件和新线描述。
type ForkResult struct {
	Branch   BranchView       `json:"branch"`
	Revision geology.Revision `json:"revision"`
}

func normalizeBranchName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if err := geology.Text("name", name, 0, 80); err != nil {
		return "", err
	}
	return name, nil
}

func (s *Service) Fork(ctx context.Context, id string, input ForkRequest) (ForkResult, error) {
	name, err := normalizeBranchName(input.Name)
	if err != nil {
		return ForkResult{}, err
	}
	reason, err := normalizedReason(input.Reason)
	if err != nil {
		return ForkResult{}, err
	}
	if input.SourceVersion < 1 {
		return ForkResult{}, geology.Invalid("source_version", "必须指定正整数来源版本")
	}
	var result ForkResult
	err = s.repo.Update(ctx, func(state *persistence.State) (bool, error) {
		history, ok := state.Histories[id]
		if !ok {
			return false, geology.Missing("剖面不存在")
		}
		source, found := revisionByVersion(history, input.SourceVersion)
		if !found {
			return false, geology.Missing("来源版本不存在")
		}
		version, err := nextVersion(history)
		if err != nil {
			return false, err
		}
		branchID, err := newID("br_")
		if err != nil {
			return false, err
		}
		for _, b := range state.Branches[id] {
			if b.Name != "" && b.Name == name {
				return false, geology.Conflict("同名修订线已存在")
			}
		}
		now := nextTime(latestEventTime(history))
		// 分叉固定来源内容，但以草拟状态开启新线，便于继续编录与稍后锁定。
		profile := source.Profile.Clone()
		profile.Version = version
		profile.Branch = branchID
		profile.State = geology.Draft
		profile.UpdatedAt = now
		event := geology.Event{
			Action:    "fork",
			Reason:    reason,
			Version:   version,
			At:        now,
			Branch:    branchID,
			ForkPoint: source.Event.Version,
			Name:      name,
		}
		revision := geology.Revision{Profile: profile, Event: event}
		if err = revision.Profile.Validate(); err != nil {
			return false, err
		}
		state.Histories[id] = append(history, revision.Clone())
		state.Branches[id] = append(state.Branches[id], persistence.Branch{
			ID:          branchID,
			Name:        name,
			ForkPoint:   source.Event.Version,
			HeadVersion: version,
			CreatedAt:   now,
		})
		result = ForkResult{
			Branch:   BranchView{ID: branchID, Name: name, ForkPoint: source.Event.Version, HeadVersion: version, CreatedAt: now, SourceSealed: source.Profile.State == geology.Sealed},
			Revision: revision,
		}
		return true, nil
	})
	return result, err
}

func latestEventTime(history []geology.Revision) time.Time {
	var latest time.Time
	for _, r := range history {
		if r.Event.At.After(latest) {
			latest = r.Event.At
		}
	}
	return latest
}

func revisionByVersion(history []geology.Revision, version int) (geology.Revision, bool) {
	for _, r := range history {
		if r.Event.Version == version {
			return r, true
		}
	}
	return geology.Revision{}, false
}

// Branches 列出剖面的全部修订线（不含 ahead/behind 关系字段）。
func (s *Service) Branches(ctx context.Context, id string) ([]BranchView, error) {
	var result []BranchView
	err := s.repo.View(ctx, func(state persistence.State) error {
		views, err := branchViews(state, id)
		if err != nil {
			return err
		}
		result = views
		return nil
	})
	return result, err
}

func branchViews(state persistence.State, id string) ([]BranchView, error) {
	branches, ok := state.Branches[id]
	if !ok {
		return nil, geology.Missing("剖面不存在")
	}
	views := make([]BranchView, 0, len(branches))
	for _, b := range branches {
		views = append(views, BranchView{
			ID: b.ID, Name: b.Name, ForkPoint: b.ForkPoint, HeadVersion: b.HeadVersion,
			IsMain: b.ID == geology.MainBranch, CreatedAt: b.CreatedAt,
		})
	}
	sort.Slice(views, func(i, j int) bool {
		if views[i].IsMain != views[j].IsMain {
			return views[i].IsMain
		}
		return views[i].ID < views[j].ID
	})
	return views, nil
}
