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

// Snapshot schema versions. Writers always persist schemaCurrent; readers
// accept every known version and migrate older shapes in memory at load
// time, so the on-disk file is upgraded by the next successful write.
const (
	// schemaV1 stores profile metadata as name, site, depth_mm and note.
	schemaV1 = 1
	// schemaV2 adds the optional recorder field to profile metadata.
	schemaV2      = 2
	schemaCurrent = schemaV2
)

// stateV1 mirrors the schema 1 layout so a v1 snapshot is decoded strictly
// against the shape its writer actually used. Fields unknown to that shape —
// including recorder — are rejected instead of being silently dropped.
type metadataV1 struct {
	Name    string `json:"name"`
	Site    string `json:"site"`
	DepthMM int64  `json:"depth_mm"`
	Note    string `json:"note"`
}

type profileV1 struct {
	ID string `json:"id"`
	metadataV1
	Layers    []geology.Layer `json:"layers"`
	State     geology.State   `json:"state"`
	Version   int             `json:"version"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
}

type revisionV1 struct {
	Profile profileV1     `json:"profile"`
	Event   geology.Event `json:"event"`
}

type stateV1 struct {
	Schema      int                           `json:"schema"`
	Histories   map[string][]revisionV1       `json:"histories"`
	Comparisons map[string]correlation.Result `json:"comparisons"`
}

// decodeSnapshot parses the snapshot payload of any known schema version.
// Each version is checked against its own shape: unknown versions and
// unknown fields fail the startup check rather than being dropped.
func decodeSnapshot(data []byte) (State, error) {
	var header struct {
		Schema int `json:"schema"`
	}
	if err := json.Unmarshal(data, &header); err != nil {
		return State{}, fmt.Errorf("invalid snapshot: %w", err)
	}
	var state State
	switch header.Schema {
	case schemaV1:
		var old stateV1
		if err := decodeStrict(data, &old); err != nil {
			return State{}, fmt.Errorf("invalid v1 snapshot: %w", err)
		}
		if old.Histories == nil || old.Comparisons == nil {
			return State{}, fmt.Errorf("unsupported v1 snapshot shape")
		}
		state = migrateV1(old)
	case schemaCurrent:
		if err := decodeStrict(data, &state); err != nil {
			return State{}, fmt.Errorf("invalid v2 snapshot: %w", err)
		}
	default:
		return State{}, fmt.Errorf("unsupported snapshot schema %d", header.Schema)
	}
	// Both shapes converge on the current in-memory State and share one
	// invariant suite: the upgrade changed no rule for fields that existed
	// in v1, and migration supplies the new field's only neutral value, so
	// old data cannot be condemned by rules written for the new shape.
	if err := state.Validate(); err != nil {
		return State{}, fmt.Errorf("snapshot validation: %w", err)
	}
	return state, nil
}

// decodeStrict decodes one JSON document into dst, rejecting unknown fields
// and trailing data instead of silently ignoring them.
func decodeStrict(data []byte, dst any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("snapshot holds more than one JSON document")
		}
		return err
	}
	return nil
}

// migrateV1 upgrades a decoded v1 snapshot to the current in-memory shape.
// The recorder field did not exist for v1 writers, so migrated profiles
// carry its neutral value, the empty string.
func migrateV1(old stateV1) State {
	state := emptyState()
	for id, revisions := range old.Histories {
		migrated := make([]geology.Revision, 0, len(revisions))
		for _, r := range revisions {
			migrated = append(migrated, geology.Revision{
				Profile: geology.Profile{
					ID: r.Profile.ID,
					Metadata: geology.Metadata{
						Name:    r.Profile.Name,
						Site:    r.Profile.Site,
						DepthMM: r.Profile.DepthMM,
						Note:    r.Profile.Note,
					},
					Layers:    r.Profile.Layers,
					State:     r.Profile.State,
					Version:   r.Profile.Version,
					CreatedAt: r.Profile.CreatedAt,
					UpdatedAt: r.Profile.UpdatedAt,
				},
				Event: r.Event,
			})
		}
		state.Histories[id] = migrated
	}
	for id, result := range old.Comparisons {
		state.Comparisons[id] = result
	}
	return state
}
