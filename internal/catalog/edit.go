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

type PatchMetadata struct {
	ExpectedVersion int                   `json:"expected_version"`
	Metadata        geology.MetadataPatch `json:"metadata"`
	Reason          string                `json:"reason"`
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

// Edit 完整替换元数据（PUT 语义）：metadata 中未给出的字段按零值写入。
func (s *Service) Edit(ctx context.Context, id string, input EditMetadata) (geology.Profile, error) {
	metadata, err := geology.NormalizeMetadata(input.Metadata)
	if err != nil {
		return geology.Profile{}, err
	}
	reason, err := normalizedReason(input.Reason)
	if err != nil {
		return geology.Profile{}, err
	}
	return s.commitMetadata(ctx, id, input.ExpectedVersion, reason, metadata)
}

// Patch 局部更新元数据（PATCH 语义）：请求中未出现的字段保持原值，
// note 可以显式清空，name 和 site 清空会在规范化校验时被拒绝。
func (s *Service) Patch(ctx context.Context, id string, input PatchMetadata) (geology.Profile, error) {
	if input.Metadata.HasFields() == 0 {
		return geology.Profile{}, geology.Invalid("metadata", "至少需要提供一个待更新字段")
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
		if err = checkMetadataEditable(state, p, input.ExpectedVersion); err != nil {
			return false, err
		}
		metadata, err := geology.NormalizeMetadata(input.Metadata.Merge(p.Metadata))
		if err != nil {
			return false, err
		}
		return s.applyMetadata(state, &p, metadata, reason, &result)
	})
	return result, err
}

// commitMetadata 是完整替换与局部更新共用的提交路径：相同的版本校验、
// 锁定边界、事件语义、版本递增和原子持久化。
func (s *Service) commitMetadata(ctx context.Context, id string, expected int, reason string, metadata geology.Metadata) (geology.Profile, error) {
	var result geology.Profile
	err := s.repo.Update(ctx, func(state *persistence.State) (bool, error) {
		p, err := state.Latest(id)
		if err != nil {
			return false, err
		}
		if err = checkMetadataEditable(state, p, expected); err != nil {
			return false, err
		}
		return s.applyMetadata(state, &p, metadata, reason, &result)
	})
	return result, err
}

// checkMetadataEditable 是元数据写入（完整替换与局部更新共用）的版本与
// 锁定校验，规则与分层替换使用的 geology.CheckEditable 完全一致：
// 版本必须命中且剖面处于草拟态。版本命中但已锁定返回普通冲突；版本落后时
// 返回携带字段变化明细的 version_conflict，帮助并发失败方看清当前版本中
// 哪些字段已经变化。
func checkMetadataEditable(state *persistence.State, p geology.Profile, expected int) error {
	if p.Version == expected && p.State == geology.Draft {
		return nil
	}
	if p.Version == expected {
		return geology.Conflict("剖面已锁定，请先重新打开")
	}
	changes := []geology.FieldChange{}
	if expected >= 1 && expected < p.Version {
		if old, err := state.Revision(p.ID, expected); err == nil {
			changes = geology.MetadataChanges(old.Profile, p)
			if old.Profile.State != p.State {
				changes = append(changes, geology.FieldChange{
					Field:  "state",
					Before: string(old.Profile.State),
					After:  string(p.State),
				})
			}
		}
	}
	return geology.StaleVersion(expected, p.Version, changes)
}

// applyMetadata 在已通过版本与锁定校验后写入元数据并生成版本事件。
func (s *Service) applyMetadata(state *persistence.State, p *geology.Profile, metadata geology.Metadata, reason string, result *geology.Profile) (bool, error) {
	before := *p
	p.Metadata = metadata
	p.Version++
	p.UpdatedAt = nextTime(p.UpdatedAt)
	changes := geology.MetadataChanges(before, *p)
	revision := geology.Revision{Profile: *p, Event: geology.Event{
		Action:  "metadata",
		Reason:  reason,
		Version: p.Version,
		At:      p.UpdatedAt,
		Changes: changes,
	}}
	if err := appendRevision(state, revision); err != nil {
		return false, err
	}
	*result = *p
	return true, nil
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
