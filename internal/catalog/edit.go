package catalog

import (
	"context"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/geology"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/persistence"
)

type EditMetadata struct {
	ExpectedVersion int              `json:"expected_version"`
	Branch          string           `json:"branch"`
	Metadata        geology.Metadata `json:"metadata"`
	Reason          string           `json:"reason"`
}

type ReplaceLayers struct {
	ExpectedVersion int             `json:"expected_version"`
	Branch          string          `json:"branch"`
	Layers          []geology.Layer `json:"layers"`
	Reason          string          `json:"reason"`
}

type StateChange struct {
	ExpectedVersion int    `json:"expected_version"`
	Branch          string `json:"branch"`
	Reason          string `json:"reason"`
}

// headRevision 解析修订线并返回其当前头修订。
func headRevision(state persistence.State, id, branch string) (string, geology.Revision, error) {
	branch, err := resolveBranch(branch)
	if err != nil {
		return branch, geology.Revision{}, err
	}
	head, err := state.HeadRevision(id, branch)
	return branch, head, err
}

func (s *Service) Edit(ctx context.Context, id string, input EditMetadata) (geology.Profile, error) {
	metadata, err := geology.NormalizeMetadata(input.Metadata)
	if err != nil {
		return geology.Profile{}, err
	}
	reason, err := normalizedReason(input.Reason)
	if err != nil {
		return geology.Profile{}, err
	}
	var result geology.Profile
	err = s.repo.Update(ctx, func(state *persistence.State) (bool, error) {
		branch, head, err := headRevision(*state, id, input.Branch)
		if err != nil {
			return false, err
		}
		p := head.Profile
		if err = geology.CheckEditable(p, input.ExpectedVersion); err != nil {
			return false, err
		}
		p.Metadata = metadata
		version, err := nextVersion(state.Histories[id])
		if err != nil {
			return false, err
		}
		p.Version = version
		p.UpdatedAt = nextTime(p.UpdatedAt)
		revision := geology.Revision{
			Profile: p,
			Event: geology.Event{
				Action: "metadata", Reason: reason, Version: version, At: p.UpdatedAt,
				Branch: branch, Parent: head.Event.Version,
			},
		}
		if err = commitRevision(state, revision, branch); err != nil {
			return false, err
		}
		result = p
		return true, nil
	})
	return result, err
}

func (s *Service) Replace(ctx context.Context, id string, input ReplaceLayers) (geology.Profile, error) {
	if input.Layers == nil {
		return geology.Profile{}, geology.Invalid("layers", "必须提供分层数组，清空时使用 []")
	}
	reason, err := normalizedReason(input.Reason)
	if err != nil {
		return geology.Profile{}, err
	}
	var result geology.Profile
	err = s.repo.Update(ctx, func(state *persistence.State) (bool, error) {
		branch, head, err := headRevision(*state, id, input.Branch)
		if err != nil {
			return false, err
		}
		p := head.Profile
		if err = geology.CheckEditable(p, input.ExpectedVersion); err != nil {
			return false, err
		}
		layers, err := geology.NormalizeLayers(input.Layers, p.DepthMM)
		if err != nil {
			return false, err
		}
		p.Layers = layers
		version, err := nextVersion(state.Histories[id])
		if err != nil {
			return false, err
		}
		p.Version = version
		p.UpdatedAt = nextTime(p.UpdatedAt)
		revision := geology.Revision{
			Profile: p,
			Event: geology.Event{
				Action: "layers", Reason: reason, Version: version, At: p.UpdatedAt,
				Branch: branch, Parent: head.Event.Version,
			},
		}
		if err = commitRevision(state, revision, branch); err != nil {
			return false, err
		}
		result = p
		return true, nil
	})
	return result, err
}

func (s *Service) Change(ctx context.Context, id string, target geology.State, input StateChange) (geology.Profile, error) {
	reason, err := normalizedReason(input.Reason)
	if err != nil {
		return geology.Profile{}, err
	}
	var result geology.Profile
	err = s.repo.Update(ctx, func(state *persistence.State) (bool, error) {
		branch, head, err := headRevision(*state, id, input.Branch)
		if err != nil {
			return false, err
		}
		p := head.Profile
		version, err := nextVersion(state.Histories[id])
		if err != nil {
			return false, err
		}
		// ChangeState 依据预期头版本校验，并用真实的新版本号生成事件。
		changed, err := geology.ChangeState(p, target, input.ExpectedVersion, version, reason, nextTime(p.UpdatedAt))
		if err != nil {
			return false, err
		}
		changed.Event.Branch = branch
		changed.Event.Parent = head.Event.Version
		if err = commitRevision(state, changed, branch); err != nil {
			return false, err
		}
		result = changed.Profile
		return true, nil
	})
	return result, err
}
