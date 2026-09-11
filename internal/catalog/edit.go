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
		p, err := state.Latest(id)
		if err != nil {
			return false, err
		}
		if err = geology.CheckEditable(p, input.ExpectedVersion); err != nil {
			return false, err
		}
		// 规范化后与当前元数据完全一致（仅输入空白差异）时没有业务变化，
		// 返回当前剖面：不写事件、不增加版本、不重写快照。
		// 放在 CheckEditable 之后，过期版本和锁定剖面仍按原规则拒绝。
		if p.Metadata == metadata {
			result = p
			return false, nil
		}
		p.Metadata = metadata
		p.Version++
		p.UpdatedAt = nextTime(p.UpdatedAt)
		revision := geology.Revision{Profile: p, Event: geology.Event{Action: "metadata", Reason: reason, Version: p.Version, At: p.UpdatedAt}}
		if err = appendRevision(state, revision); err != nil {
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
		p, err := state.Latest(id)
		if err != nil {
			return false, err
		}
		if err = geology.CheckEditable(p, input.ExpectedVersion); err != nil {
			return false, err
		}
		layers, err := geology.NormalizeLayers(input.Layers, p.DepthMM)
		if err != nil {
			return false, err
		}
		// 规范化（trim 文字、按深度排序）后与当前分层一致，仅输入顺序或
		// 首尾空白不同，属于同一套岩层：不记修订。仍排在可编辑性和分层
		// 校验之后，冲突与无效输入不会因此被放过。
		if geology.LayersEqual(p.Layers, layers) {
			result = p
			return false, nil
		}
		p.Layers = layers
		p.Version++
		p.UpdatedAt = nextTime(p.UpdatedAt)
		revision := geology.Revision{Profile: p, Event: geology.Event{Action: "layers", Reason: reason, Version: p.Version, At: p.UpdatedAt}}
		if err = appendRevision(state, revision); err != nil {
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
		p, err := state.Latest(id)
		if err != nil {
			return false, err
		}
		revision, err := geology.ChangeState(p, target, input.ExpectedVersion, reason, nextTime(p.UpdatedAt))
		if err != nil {
			return false, err
		}
		if err = appendRevision(state, revision); err != nil {
			return false, err
		}
		result = revision.Profile
		return true, nil
	})
	return result, err
}
