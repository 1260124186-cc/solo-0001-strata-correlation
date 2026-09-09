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
