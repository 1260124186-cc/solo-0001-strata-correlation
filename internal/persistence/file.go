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
	"time"
)

const maxSnapshot = 64 << 20

type envelope struct {
	Digest string          `json:"digest"`
	Data   json.RawMessage `json:"data"`
}

// legacyState 是引入并行修订线之前的快照形状：只有线性历史，无分叉登记。
type legacyState struct {
	Schema      int                           `json:"schema"`
	Histories   map[string][]geology.Revision `json:"histories"`
	Comparisons map[string]json.RawMessage    `json:"comparisons"`
}

func readSnapshot(path string) (State, error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return emptyState(), nil
	}
	if err != nil {
		return State{}, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, maxSnapshot+1))
	if err != nil {
		return State{}, err
	}
	if len(raw) > maxSnapshot {
		return State{}, fmt.Errorf("snapshot exceeds 64 MiB")
	}
	var env envelope
	if err = json.Unmarshal(raw, &env); err != nil {
		return State{}, fmt.Errorf("invalid snapshot: %w", err)
	}
	sum := sha256.Sum256(env.Data)
	if env.Digest != hex.EncodeToString(sum[:]) {
		return State{}, fmt.Errorf("snapshot checksum mismatch")
	}
	var header struct {
		Schema int `json:"schema"`
	}
	if err = json.Unmarshal(env.Data, &header); err != nil {
		return State{}, fmt.Errorf("invalid snapshot: %w", err)
	}
	var state State
	switch header.Schema {
	case 1:
		state, err = migrateLegacy(env.Data)
	case 2:
		if err = json.Unmarshal(env.Data, &state); err != nil {
			return State{}, err
		}
	default:
		return State{}, fmt.Errorf("unsupported snapshot schema %d", header.Schema)
	}
	if err != nil {
		return State{}, err
	}
	if err = state.Validate(); err != nil {
		return State{}, fmt.Errorf("snapshot validation: %w", err)
	}
	return state, nil
}

// migrateLegacy 不改动磁盘上的旧快照，只在内存里把顺序历史解释为单一主线：
// 事件与剖面补 branch=main、parent 按原顺序链接，并登记主线头。
// 旧版本号就是真实版本号，不需要重新登记。
func migrateLegacy(data []byte) (State, error) {
	var legacy legacyState
	if err := json.Unmarshal(data, &legacy); err != nil {
		return State{}, err
	}
	state := emptyState()
	for id, history := range legacy.Histories {
		if len(history) == 0 {
			return State{}, fmt.Errorf("empty history %s", id)
		}
		migrated := make([]geology.Revision, len(history))
		var previousTime time.Time
		for i, r := range history {
			if r.Profile.ID != id || r.Profile.Version != i+1 || r.Event.Version != i+1 {
				return State{}, fmt.Errorf("inconsistent legacy revision %s/%d", id, i+1)
			}
			r.Profile.Branch = geology.MainBranch
			r.Event.Branch = geology.MainBranch
			if i == 0 {
				if r.Event.Action != "create" || r.Profile.State != geology.Draft {
					return State{}, fmt.Errorf("invalid legacy initial revision")
				}
			} else {
				r.Event.Parent = i
			}
			if i > 0 && r.Event.At.Before(previousTime) {
				return State{}, fmt.Errorf("invalid legacy chronology")
			}
			previousTime = r.Event.At
			migrated[i] = r
		}
		state.Histories[id] = migrated
		state.Branches[id] = []Branch{{
			ID:          geology.MainBranch,
			HeadVersion: migrated[len(migrated)-1].Event.Version,
		}}
	}
	// 对比结果按原算法校验可重复性，因此需要保留原样并复用相同编号。
	if legacy.Comparisons != nil {
		raw, err := json.Marshal(legacy.Comparisons)
		if err != nil {
			return State{}, err
		}
		if err = json.Unmarshal(raw, &state.Comparisons); err != nil {
			return State{}, err
		}
	}
	return state, nil
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
