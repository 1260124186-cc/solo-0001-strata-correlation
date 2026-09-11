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

// snapshotLoad describes every phase readSnapshot executes. Stages use the
// same checks and error texts as normal startup so the diagnostic report can
// never diverge from the Open decision.
type snapshotLoad struct {
	present    bool
	size       int64
	stage      string
	state      State
	checksumOK bool
	err        error
}

const (
	stageRead       = "read"
	stageCapacity   = "capacity"
	stageEnvelope   = "envelope"
	stageChecksum   = "checksum"
	stageDecode     = "decode"
	stageValidation = "validation"
)

func loadSnapshot(path string) snapshotLoad {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return snapshotLoad{state: emptyState(), checksumOK: true}
	}
	load := snapshotLoad{present: true}
	if err != nil {
		load.stage, load.err = stageRead, err
		return load
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		load.stage, load.err = stageRead, err
		return load
	}
	load.size = info.Size()
	raw, err := io.ReadAll(io.LimitReader(f, maxSnapshot+1))
	if err != nil {
		load.stage, load.err = stageRead, err
		return load
	}
	if len(raw) > maxSnapshot {
		load.stage, load.err = stageCapacity, fmt.Errorf("snapshot exceeds 64 MiB")
		return load
	}
	var env envelope
	if err = json.Unmarshal(raw, &env); err != nil {
		load.stage, load.err = stageEnvelope, fmt.Errorf("invalid snapshot: %w", err)
		return load
	}
	sum := sha256.Sum256(env.Data)
	if env.Digest != hex.EncodeToString(sum[:]) {
		load.checksumOK = false
		load.stage, load.err = stageChecksum, fmt.Errorf("snapshot checksum mismatch")
		return load
	}
	load.checksumOK = true
	if err = json.Unmarshal(env.Data, &load.state); err != nil {
		load.stage, load.err = stageDecode, err
		return load
	}
	if err = load.state.Validate(); err != nil {
		load.stage, load.err = stageValidation, fmt.Errorf("snapshot validation: %w", err)
		return load
	}
	return load
}

func readSnapshot(path string) (State, error) {
	load := loadSnapshot(path)
	return load.state, load.err
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
