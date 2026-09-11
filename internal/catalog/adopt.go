package catalog

import (
	"context"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/geology"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/persistence"
)

type AdoptHistory struct {
	ExpectedVersion int    `json:"expected_version"`
	SourceVersion   int    `json:"source_version"`
	Reason          string `json:"reason"`
}

func (s *Service) Adopt(ctx context.Context, id string, input AdoptHistory) (geology.Profile, error) {
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
		source, err := state.Revision(id, input.SourceVersion)
		if err != nil {
			return false, err
		}
		revision, err := geology.Adopt(p, source.Profile, input.ExpectedVersion, input.SourceVersion, reason, nextTime(p.UpdatedAt))
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
