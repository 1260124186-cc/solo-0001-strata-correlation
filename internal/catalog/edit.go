package catalog

import (
	"context"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/geology"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/persistence"
)

type EditMetadata struct {
	ExpectedVersion int              `json:"expected_version"`
	Metadata        geology.Metadata `json:"metadata"`
	Reason          string           `json:"reason"`
}

type ReplaceLayers struct {
	ExpectedVersion int             `json:"expected_version"`
	Layers          []geology.Layer `json:"layers"`
	Reason          string          `json:"reason"`
}

type StateChange struct {
	ExpectedVersion int    `json:"expected_version"`
	Reason          string `json:"reason"`
}

// applyEdit 校验并追加一次元数据修订，是批量与单份路径共用的原子步骤。
func applyEdit(state *persistence.State, id string, input EditMetadata) (geology.Profile, error) {
	metadata, err := geology.NormalizeMetadata(input.Metadata)
	if err != nil {
		return geology.Profile{}, err
	}
	reason, err := normalizedReason(input.Reason)
	if err != nil {
		return geology.Profile{}, err
	}
	p, err := state.Latest(id)
	if err != nil {
		return geology.Profile{}, err
	}
	if err = geology.CheckEditable(p, input.ExpectedVersion); err != nil {
		return geology.Profile{}, err
	}
	p.Metadata = metadata
	p.Version++
	p.UpdatedAt = nextTime(p.UpdatedAt)
	revision := geology.Revision{Profile: p, Event: geology.Event{Action: "metadata", Reason: reason, Version: p.Version, At: p.UpdatedAt}}
	if err = appendRevision(state, revision); err != nil {
		return geology.Profile{}, err
	}
	return p, nil
}

// applyReplace 校验并追加一次分层整体替换，是批量与单份路径共用的原子步骤。
func applyReplace(state *persistence.State, id string, input ReplaceLayers) (geology.Profile, error) {
	if input.Layers == nil {
		return geology.Profile{}, geology.Invalid("layers", "必须提供分层数组，清空时使用 []")
	}
	reason, err := normalizedReason(input.Reason)
	if err != nil {
		return geology.Profile{}, err
	}
	p, err := state.Latest(id)
	if err != nil {
		return geology.Profile{}, err
	}
	if err = geology.CheckEditable(p, input.ExpectedVersion); err != nil {
		return geology.Profile{}, err
	}
	layers, err := geology.NormalizeLayers(input.Layers, p.DepthMM)
	if err != nil {
		return geology.Profile{}, err
	}
	p.Layers = layers
	p.Version++
	p.UpdatedAt = nextTime(p.UpdatedAt)
	revision := geology.Revision{Profile: p, Event: geology.Event{Action: "layers", Reason: reason, Version: p.Version, At: p.UpdatedAt}}
	if err = appendRevision(state, revision); err != nil {
		return geology.Profile{}, err
	}
	return p, nil
}

// applyChange 校验并追加一次状态流转，是批量与单份路径共用的原子步骤。
func applyChange(state *persistence.State, id string, target geology.State, input StateChange) (geology.Profile, error) {
	reason, err := normalizedReason(input.Reason)
	if err != nil {
		return geology.Profile{}, err
	}
	p, err := state.Latest(id)
	if err != nil {
		return geology.Profile{}, err
	}
	revision, err := geology.ChangeState(p, target, input.ExpectedVersion, reason, nextTime(p.UpdatedAt))
	if err != nil {
		return geology.Profile{}, err
	}
	if err = appendRevision(state, revision); err != nil {
		return geology.Profile{}, err
	}
	return revision.Profile, nil
}

func (s *Service) Edit(ctx context.Context, id string, input EditMetadata) (geology.Profile, error) {
	var result geology.Profile
	err := s.repo.Update(ctx, func(state *persistence.State) (bool, error) {
		p, err := applyEdit(state, id, input)
		if err != nil {
			return false, err
		}
		result = p
		return true, nil
	})
	return result, err
}

func (s *Service) Replace(ctx context.Context, id string, input ReplaceLayers) (geology.Profile, error) {
	var result geology.Profile
	err := s.repo.Update(ctx, func(state *persistence.State) (bool, error) {
		p, err := applyReplace(state, id, input)
		if err != nil {
			return false, err
		}
		result = p
		return true, nil
	})
	return result, err
}

func (s *Service) Change(ctx context.Context, id string, target geology.State, input StateChange) (geology.Profile, error) {
	var result geology.Profile
	err := s.repo.Update(ctx, func(state *persistence.State) (bool, error) {
		p, err := applyChange(state, id, target, input)
		if err != nil {
			return false, err
		}
		result = p
		return true, nil
	})
	return result, err
}
