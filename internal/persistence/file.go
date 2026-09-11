package persistence

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const maxSnapshot = 64 << 20

type envelope struct {
	Digest string          `json:"digest"`
	Data   json.RawMessage `json:"data"`
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
	if err = decodeStrict(raw, &env); err != nil {
		return State{}, fmt.Errorf("invalid snapshot: %w", err)
	}
	sum := sha256.Sum256(env.Data)
	if env.Digest != hex.EncodeToString(sum[:]) {
		return State{}, fmt.Errorf("snapshot checksum mismatch")
	}
	state, err := decodeState(env.Data)
	if err != nil {
		return State{}, err
	}
	if err = state.Validate(); err != nil {
		return State{}, fmt.Errorf("snapshot validation: %w", err)
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
