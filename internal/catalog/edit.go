package catalog

import (
	"context"

	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/geology"
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
	return s.commitRevision(ctx, id, input.ExpectedVersion, &metadataRevision{metadata: input.Metadata, why: input.Reason})
}

func (s *Service) Replace(ctx context.Context, id string, input ReplaceLayers) (geology.Profile, error) {
	return s.commitRevision(ctx, id, input.ExpectedVersion, &layersRevision{layers: input.Layers, why: input.Reason})
}

func (s *Service) Change(ctx context.Context, id string, target geology.State, input StateChange) (geology.Profile, error) {
	return s.commitRevision(ctx, id, input.ExpectedVersion, &stateRevision{target: target, why: input.Reason})
}
