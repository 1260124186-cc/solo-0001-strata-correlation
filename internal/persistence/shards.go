package persistence

import (
	"encoding/json"
	"fmt"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/correlation"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/geology"
	"io"
	"os"
	"path/filepath"
	"sort"
)

const (
	shardsDir = "shards"

	shardFormat = 2
	kindProfile = "profile"
	kindCompare = "comparison"
)

type shardEnvelope struct {
	Format int             `json:"format"`
	Kind   string          `json:"kind"`
	ID     string          `json:"id"`
	Digest string          `json:"digest"`
	Data   json.RawMessage `json:"data"`
}

type profileShard struct {
	Schema  int                `json:"schema"`
	History []geology.Revision `json:"history"`
}

type comparisonShard struct {
	Schema     int                `json:"schema"`
	Comparison correlation.Result `json:"comparison"`
}

func shardName(id string) string { return id + ".json" }

func encodeProfileShard(id string, history []geology.Revision) ([]byte, error) {
	data, err := json.Marshal(profileShard{Schema: 1, History: history})
	if err != nil {
		return nil, err
	}
	return encodeShard(kindProfile, id, data)
}

func encodeComparisonShard(result correlation.Result) ([]byte, error) {
	data, err := json.Marshal(comparisonShard{Schema: 1, Comparison: result})
	if err != nil {
		return nil, err
	}
	return encodeShard(kindCompare, result.ID, data)
}

func encodeShard(kind, id string, data []byte) ([]byte, error) {
	return json.Marshal(shardEnvelope{
		Format: shardFormat,
		Kind:   kind,
		ID:     id,
		Digest: digestOf(data),
		Data:   data,
	})
}

// readShard decodes one shard file. It verifies envelope shape, digest, kind
// and that the file name agrees with the contained identity, so a partial or
// mislabeled file can never silently contribute data to the recovered state.
func readShard(path string) (kind, id string, data []byte, err error) {
	f, err := os.Open(path)
	if err != nil {
		return "", "", nil, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, maxShard+1))
	if err != nil {
		return "", "", nil, err
	}
	if len(raw) > maxShard {
		return "", "", nil, fmt.Errorf("shard %s exceeds 64 MiB", filepath.Base(path))
	}
	var env shardEnvelope
	if err = json.Unmarshal(raw, &env); err != nil {
		return "", "", nil, fmt.Errorf("invalid shard %s: %w", filepath.Base(path), err)
	}
	if env.Format != shardFormat || (env.Kind != kindProfile && env.Kind != kindCompare) || env.ID == "" {
		return "", "", nil, fmt.Errorf("invalid shard %s: unsupported envelope", filepath.Base(path))
	}
	if shardName(env.ID) != filepath.Base(path) {
		return "", "", nil, fmt.Errorf("invalid shard %s: name does not match id %s", filepath.Base(path), env.ID)
	}
	if env.Kind == kindProfile && !geology.ValidID(env.ID, "prf_") {
		return "", "", nil, fmt.Errorf("invalid shard %s: bad profile id", filepath.Base(path))
	}
	if env.Kind == kindCompare && !geology.ValidID(env.ID, "cmp_") {
		return "", "", nil, fmt.Errorf("invalid shard %s: bad comparison id", filepath.Base(path))
	}
	if err = verifyDigest(env.Digest, env.Data); err != nil {
		return "", "", nil, fmt.Errorf("shard %s: %w", filepath.Base(path), err)
	}
	return env.Kind, env.ID, env.Data, nil
}

// loadShards reconstructs state from the shard directory. Every regular file
// in it must be a well-named, intact shard; leftovers are swept before this
// runs, so any other entry makes startup fail instead of being ignored.
func loadShards(dir string) (State, error) {
	state := emptyState()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return State{}, err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			return State{}, fmt.Errorf("unexpected directory %s in shard store", entry.Name())
		}
		kind, id, data, err := readShard(filepath.Join(dir, entry.Name()))
		if err != nil {
			return State{}, err
		}
		switch kind {
		case kindProfile:
			var shard profileShard
			if err = json.Unmarshal(data, &shard); err != nil {
				return State{}, fmt.Errorf("invalid shard %s: %w", entry.Name(), err)
			}
			if shard.Schema != 1 || len(shard.History) == 0 {
				return State{}, fmt.Errorf("invalid shard %s: unsupported profile shape", entry.Name())
			}
			if _, exists := state.Histories[id]; exists {
				return State{}, fmt.Errorf("duplicate profile shard %s", id)
			}
			state.Histories[id] = shard.History
		case kindCompare:
			var shard comparisonShard
			if err = json.Unmarshal(data, &shard); err != nil {
				return State{}, fmt.Errorf("invalid shard %s: %w", entry.Name(), err)
			}
			if shard.Schema != 1 {
				return State{}, fmt.Errorf("invalid shard %s: unsupported comparison shape", entry.Name())
			}
			if _, exists := state.Comparisons[id]; exists {
				return State{}, fmt.Errorf("duplicate comparison shard %s", id)
			}
			state.Comparisons[id] = shard.Comparison
		}
	}
	if err = state.Validate(); err != nil {
		return State{}, fmt.Errorf("shard validation: %w", err)
	}
	return state, nil
}

// migrateSnapshot writes one shard per profile and comparison from a legacy
// whole snapshot and then removes the snapshot file. Every step is idempotent
// and crashes at any point resolve to a single outcome on restart: existing
// shards are overwritten atomically, and the snapshot is deleted only after
// all shards landed, so its presence simply triggers another replay.
func migrateSnapshot(shardDir, snapshotPath string, legacy State) error {
	profileIDs := make([]string, 0, len(legacy.Histories))
	for id := range legacy.Histories {
		profileIDs = append(profileIDs, id)
	}
	sort.Strings(profileIDs)
	for _, id := range profileIDs {
		contents, err := encodeProfileShard(id, legacy.Histories[id])
		if err != nil {
			return err
		}
		if _, err = writeFileAtomic(shardDir, shardName(id), contents); err != nil {
			return err
		}
	}
	comparisonIDs := make([]string, 0, len(legacy.Comparisons))
	for id := range legacy.Comparisons {
		comparisonIDs = append(comparisonIDs, id)
	}
	sort.Strings(comparisonIDs)
	for _, id := range comparisonIDs {
		contents, err := encodeComparisonShard(legacy.Comparisons[id])
		if err != nil {
			return err
		}
		if _, err = writeFileAtomic(shardDir, shardName(id), contents); err != nil {
			return err
		}
	}
	if err := os.Remove(snapshotPath); err != nil {
		return err
	}
	return syncDir(filepath.Dir(snapshotPath))
}
