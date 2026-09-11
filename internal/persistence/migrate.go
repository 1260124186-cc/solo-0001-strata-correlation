package persistence

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/correlation"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/geology"
	"io"
	"time"
)

// stateV1 mirrors the schema 1 snapshot layout, written before profiles
// carried a source field. Each supported schema is decoded against its own
// layout and checked by that layout's rules; unknown fields are rejected
// instead of silently dropped.
type stateV1 struct {
	Schema      int                           `json:"schema"`
	Histories   map[string][]revisionV1       `json:"histories"`
	Comparisons map[string]correlation.Result `json:"comparisons"`
}

type revisionV1 struct {
	Profile profileV1     `json:"profile"`
	Event   geology.Event `json:"event"`
}

type profileV1 struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Site      string          `json:"site"`
	DepthMM   int64           `json:"depth_mm"`
	Note      string          `json:"note"`
	Layers    []geology.Layer `json:"layers"`
	State     geology.State   `json:"state"`
	Version   int             `json:"version"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
}

// decodeState converts a snapshot payload of any supported schema into the
// current in-memory state, so readers never deal with layout versions.
func decodeState(data []byte) (State, error) {
	var head struct {
		Schema int `json:"schema"`
	}
	if err := json.Unmarshal(data, &head); err != nil {
		return State{}, fmt.Errorf("invalid snapshot data: %w", err)
	}
	switch head.Schema {
	case 1:
		var old stateV1
		if err := decodeStrict(data, &old); err != nil {
			return State{}, fmt.Errorf("invalid schema 1 snapshot: %w", err)
		}
		if old.Histories == nil || old.Comparisons == nil {
			return State{}, fmt.Errorf("invalid schema 1 snapshot: unsupported snapshot shape")
		}
		return upgradeV1(old), nil
	case currentSchema:
		var state State
		if err := decodeStrict(data, &state); err != nil {
			return State{}, fmt.Errorf("invalid schema %d snapshot: %w", currentSchema, err)
		}
		return state, nil
	default:
		return State{}, fmt.Errorf("unsupported snapshot schema %d", head.Schema)
	}
}

// upgradeV1 migrates a schema 1 snapshot to the current layout. Profiles gain
// an empty source field, meaning the source was never recorded.
func upgradeV1(old stateV1) State {
	state := emptyState()
	for id, history := range old.Histories {
		revisions := make([]geology.Revision, len(history))
		for i, r := range history {
			p := r.Profile
			revisions[i] = geology.Revision{
				Profile: geology.Profile{
					ID: p.ID,
					Metadata: geology.Metadata{
						Name:    p.Name,
						Site:    p.Site,
						DepthMM: p.DepthMM,
						Note:    p.Note,
					},
					Layers:    p.Layers,
					State:     p.State,
					Version:   p.Version,
					CreatedAt: p.CreatedAt,
					UpdatedAt: p.UpdatedAt,
				},
				Event: r.Event,
			}
		}
		state.Histories[id] = revisions
	}
	for id, result := range old.Comparisons {
		state.Comparisons[id] = result
	}
	return state
}

// decodeStrict decodes exactly one JSON document and rejects unknown fields,
// so a snapshot written by a newer service fails loudly at startup instead of
// being silently truncated on the next write.
func decodeStrict(data []byte, dst any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err != nil {
			return err
		}
		return fmt.Errorf("unexpected trailing data")
	}
	return nil
}
