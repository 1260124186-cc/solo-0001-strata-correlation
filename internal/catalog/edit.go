package catalog

import (
	"context"
	"time"

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

// applyMetadata、applyLayers 和 applyStateChange 只读写传入的状态，
// 供单剖面接口和批量修订共用，保证两种入口的校验规则完全一致。
func applyMetadata(state *persistence.State, id string, expected int, metadata geology.Metadata, reason string, now time.Time) (geology.Profile, error) {
	p, err := state.Latest(id)
	if err != nil {
		return geology.Profile{}, err
	}
	if err = geology.CheckEditable(p, expected); err != nil {
		return geology.Profile{}, err
	}
	p.Metadata = metadata
	p.Version++
	p.UpdatedAt = nextTimeAt(p.UpdatedAt, now)
	revision := geology.Revision{Profile: p, Event: geology.Event{Action: "metadata", Reason: reason, Version: p.Version, At: p.UpdatedAt}}
	if err = appendRevision(state, revision); err != nil {
		return geology.Profile{}, err
	}
	return p, nil
}

func applyLayers(state *persistence.State, id string, expected int, layers []geology.Layer, reason string, now time.Time) (geology.Profile, error) {
	p, err := state.Latest(id)
	if err != nil {
		return geology.Profile{}, err
	}
	if err = geology.CheckEditable(p, expected); err != nil {
		return geology.Profile{}, err
	}
	normalized, err := geology.NormalizeLayers(layers, p.DepthMM)
	if err != nil {
		return geology.Profile{}, err
	}
	p.Layers = normalized
	p.Version++
	p.UpdatedAt = nextTimeAt(p.UpdatedAt, now)
	revision := geology.Revision{Profile: p, Event: geology.Event{Action: "layers", Reason: reason, Version: p.Version, At: p.UpdatedAt}}
	if err = appendRevision(state, revision); err != nil {
		return geology.Profile{}, err
	}
	return p, nil
}

func applyStateChange(state *persistence.State, id string, target geology.State, expected int, reason string, now time.Time) (geology.Profile, error) {
	p, err := state.Latest(id)
	if err != nil {
		return geology.Profile{}, err
	}
	revision, err := geology.ChangeState(p, target, expected, reason, nextTimeAt(p.UpdatedAt, now))
	if err != nil {
		return geology.Profile{}, err
	}
	if err = appendRevision(state, revision); err != nil {
		return geology.Profile{}, err
	}
	return revision.Profile, nil
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
		p, err := applyMetadata(state, id, input.ExpectedVersion, metadata, reason, time.Now().UTC())
		if err != nil {
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
		p, err := applyLayers(state, id, input.ExpectedVersion, input.Layers, reason, time.Now().UTC())
		if err != nil {
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
		p, err := applyStateChange(state, id, target, input.ExpectedVersion, reason, time.Now().UTC())
		if err != nil {
			return false, err
		}
		result = p
		return true, nil
	})
	return result, err
}
