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

// MaxSnapshotBytes bounds the finished on-disk snapshot file, checksum
// envelope included. Writes are gated on the same byte figure reported by
// SnapshotSize.
const MaxSnapshotBytes = 64 << 20

type envelope struct {
	Digest string          `json:"digest"`
	Data   json.RawMessage `json:"data"`
}

// SnapshotTooLargeError means the finished snapshot file (checksum envelope
// included) would exceed MaxSnapshotBytes. It is reported before any file in
// the data directory is created or replaced.
type SnapshotTooLargeError struct {
	Bytes int
	Limit int
}

func (e *SnapshotTooLargeError) Error() string {
	return fmt.Sprintf("snapshot capacity of %d bytes reached, snapshot requires %d bytes", e.Limit, e.Bytes)
}

// marshalSnapshot renders state in the exact byte shape stored on disk. data is
// the state payload by itself; contents is the checksum envelope that actually
// lands in the file and is always larger than data. Every size decision must be
// based on contents, never on data.
func marshalSnapshot(state State) (data, contents []byte, err error) {
	data, err = json.Marshal(state)
	if err != nil {
		return nil, nil, err
	}
	sum := sha256.Sum256(data)
	contents, err = json.Marshal(envelope{Digest: hex.EncodeToString(sum[:]), Data: data})
	if err != nil {
		return nil, nil, err
	}
	return data, contents, nil
}

// SnapshotSize reports snapshot sizes from the real file caliber: fileBytes
// counts the checksum envelope that commit gating uses, dataBytes counts only
// the marshaled state payload. Diagnostics and write gating share this function
// so they can never disagree.
func SnapshotSize(state State) (dataBytes, fileBytes int, err error) {
	data, contents, err := marshalSnapshot(state)
	if err != nil {
		return 0, 0, err
	}
	return len(data), len(contents), nil
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
	raw, err := io.ReadAll(io.LimitReader(f, MaxSnapshotBytes+1))
	if err != nil {
		return State{}, err
	}
	if len(raw) > MaxSnapshotBytes {
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
	var state State
	if err = json.Unmarshal(env.Data, &state); err != nil {
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
	_, contents, err := marshalSnapshot(state)
	if err != nil {
		return false, err
	}
	// Measure the finished file and reject before touching the data directory,
	// so an over-limit write creates no temporary file and leaves the previous
	// snapshot and in-memory state untouched.
	if len(contents) > MaxSnapshotBytes {
		return false, &SnapshotTooLargeError{Bytes: len(contents), Limit: MaxSnapshotBytes}
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
