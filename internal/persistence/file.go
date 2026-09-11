package persistence

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const maxSnapshot = 64 << 20

var errSnapshotChangedDuringOpen = errors.New("snapshot changed during open")

type envelope struct {
	Digest string          `json:"digest"`
	Data   json.RawMessage `json:"data"`
}

type snapshotIdentity struct {
	device      uint64
	file        uint64
	size        int64
	modTimeNano int64
}

func readSnapshot(path string) (state State, identity snapshotIdentity, err error) {
	for attempt := 0; attempt < 3; attempt++ {
		state, identity, err = loadSnapshot(path)
		if !errors.Is(err, errSnapshotChangedDuringOpen) {
			return state, identity, err
		}
	}
	return State{}, snapshotIdentity{}, err
}

func loadSnapshot(path string) (State, snapshotIdentity, error) {
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return emptyState(), snapshotIdentity{}, nil
	}
	if err != nil {
		return State{}, snapshotIdentity{}, err
	}
	identity := snapshotIdentityFromInfo(info)
	f, err := os.Open(path)
	if err != nil {
		return State{}, snapshotIdentity{}, err
	}
	defer f.Close()
	openedInfo, err := f.Stat()
	if err != nil {
		return State{}, snapshotIdentity{}, err
	}
	if snapshotIdentityFromInfo(openedInfo) != identity {
		return State{}, snapshotIdentity{}, errSnapshotChangedDuringOpen
	}
	raw, err := io.ReadAll(io.LimitReader(f, maxSnapshot+1))
	if err != nil {
		return State{}, snapshotIdentity{}, err
	}
	finishedInfo, err := f.Stat()
	if err != nil {
		return State{}, snapshotIdentity{}, err
	}
	if snapshotIdentityFromInfo(finishedInfo) != identity {
		return State{}, snapshotIdentity{}, errSnapshotChangedDuringOpen
	}
	if len(raw) > maxSnapshot {
		return State{}, snapshotIdentity{}, fmt.Errorf("snapshot exceeds 64 MiB")
	}
	var env envelope
	if err = json.Unmarshal(raw, &env); err != nil {
		return State{}, snapshotIdentity{}, fmt.Errorf("invalid snapshot: %w", err)
	}
	sum := sha256.Sum256(env.Data)
	if env.Digest != hex.EncodeToString(sum[:]) {
		return State{}, snapshotIdentity{}, fmt.Errorf("snapshot checksum mismatch")
	}
	var state State
	if err = json.Unmarshal(env.Data, &state); err != nil {
		return State{}, snapshotIdentity{}, err
	}
	if err = state.Validate(); err != nil {
		return State{}, snapshotIdentity{}, fmt.Errorf("snapshot validation: %w", err)
	}
	return state, identity, nil
}

// A rename is the commit point. After it, callers must use the new state even
// when syncing the directory reports an error.
func writeSnapshot(path string, state State) (identity snapshotIdentity, committed bool, err error) {
	raw, err := json.Marshal(state)
	if err != nil {
		return snapshotIdentity{}, false, err
	}
	sum := sha256.Sum256(raw)
	contents, err := json.Marshal(envelope{hex.EncodeToString(sum[:]), raw})
	if err != nil {
		return snapshotIdentity{}, false, err
	}
	if len(contents) > maxSnapshot {
		return snapshotIdentity{}, false, fmt.Errorf("snapshot capacity of 64 MiB reached")
	}
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".strata-*")
	if err != nil {
		return snapshotIdentity{}, false, err
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
		return snapshotIdentity{}, false, err
	}
	if closeErr != nil {
		return snapshotIdentity{}, false, closeErr
	}
	if err = os.Rename(temporary, path); err != nil {
		return snapshotIdentity{}, false, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return snapshotIdentity{}, true, err
	}
	identity = snapshotIdentityFromInfo(info)
	d, err := os.Open(filepath.Dir(path))
	if err != nil {
		return identity, true, err
	}
	defer d.Close()
	if err = d.Sync(); err != nil {
		return identity, true, err
	}
	return identity, true, nil
}
