package persistence

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/geology"
	"io"
	"os"
	"path/filepath"
)

const maxSnapshot = 64 << 20

type envelope struct {
	Digest string          `json:"digest"`
	Data   json.RawMessage `json:"data"`
}

func readSnapshot(path string) (State, bool, error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return emptyState(), false, nil
	}
	if err != nil {
		return State{}, false, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, maxSnapshot+1))
	if err != nil {
		return State{}, false, err
	}
	if len(raw) > maxSnapshot {
		return State{}, false, fmt.Errorf("snapshot exceeds 64 MiB")
	}
	var env envelope
	if err = json.Unmarshal(raw, &env); err != nil {
		return State{}, false, fmt.Errorf("invalid snapshot: %w", err)
	}
	sum := sha256.Sum256(env.Data)
	if env.Digest != hex.EncodeToString(sum[:]) {
		return State{}, false, fmt.Errorf("snapshot checksum mismatch")
	}
	var head struct {
		Schema int `json:"schema"`
	}
	if err = json.Unmarshal(env.Data, &head); err != nil {
		return State{}, false, fmt.Errorf("invalid snapshot: %w", err)
	}
	migrated := false
	data := env.Data
	if head.Schema == 1 {
		data, err = migrateV1(env.Data)
		if err != nil {
			return State{}, false, err
		}
		migrated = true
	}
	var state State
	if err = json.Unmarshal(data, &state); err != nil {
		return State{}, false, err
	}
	if err = state.Validate(); err != nil {
		return State{}, false, fmt.Errorf("snapshot validation: %w", err)
	}
	return state, migrated, nil
}

// migrateV1 把旧版快照中的全部剖面归入默认研究区。
// 剖面、历史版本和对比结果原样保留，只增加归属与配额维度。
func migrateV1(data json.RawMessage) (json.RawMessage, error) {
	var legacy struct {
		Histories   map[string]json.RawMessage `json:"histories"`
		Comparisons map[string]json.RawMessage `json:"comparisons"`
	}
	if err := json.Unmarshal(data, &legacy); err != nil {
		return nil, fmt.Errorf("invalid v1 snapshot: %w", err)
	}
	memberships := make(map[string]string, len(legacy.Histories))
	for id := range legacy.Histories {
		memberships[id] = geology.DefaultAreaID
	}
	migrated := struct {
		Schema      int                        `json:"schema"`
		Areas       map[string]geology.Area    `json:"areas"`
		Memberships map[string]string          `json:"memberships"`
		Histories   map[string]json.RawMessage `json:"histories"`
		Comparisons map[string]json.RawMessage `json:"comparisons"`
	}{
		Schema:      2,
		Areas:       map[string]geology.Area{geology.DefaultAreaID: defaultArea()},
		Memberships: memberships,
		Histories:   legacy.Histories,
		Comparisons: legacy.Comparisons,
	}
	if migrated.Histories == nil {
		migrated.Histories = map[string]json.RawMessage{}
	}
	if migrated.Comparisons == nil {
		migrated.Comparisons = map[string]json.RawMessage{}
	}
	raw, err := json.Marshal(migrated)
	if err != nil {
		return nil, err
	}
	return raw, nil
}

// A rename is the commit point. After it, callers must use the new state even
// when syncing the directory reports an error.
func writeSnapshot(path string, state State) (committed bool, err error) {
	raw, err := json.Marshal(state)
	if err != nil {
		return false, err
	}
	sum := sha256.Sum256(raw)
	contents, err := json.Marshal(envelope{hex.EncodeToString(sum[:]), raw})
	if err != nil {
		return false, err
	}
	if len(contents) > maxSnapshot {
		return false, fmt.Errorf("snapshot capacity of 64 MiB reached")
	}
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".strata-*")
	if err != nil {
		return false, err
	}
	temporary := f.Name()
	defer os.Remove(temporary)
	if err = f.Chmod(0600); err == nil {
		var n int
		n, err = f.Write(contents)
		if err == nil && n != len(contents) {
			err = io.ErrShortWrite
		}
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return false, err
	}
	if closeErr != nil {
		return false, closeErr
	}
	if err = os.Rename(temporary, path); err != nil {
		return false, err
	}
	d, err := os.Open(dir)
	if err != nil {
		return true, err
	}
	defer d.Close()
	return true, d.Sync()
}
