package persistence

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	maxSnapshot = 64 << 20
	maxShard    = 64 << 20
	tempPrefix  = ".strata-"
)

// envelope is the legacy whole-snapshot container, kept readable so existing
// data directories migrate without an export/import step.
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
	if err = json.Unmarshal(raw, &env); err != nil {
		return State{}, fmt.Errorf("invalid snapshot: %w", err)
	}
	if err = verifyDigest(env.Digest, env.Data); err != nil {
		return State{}, err
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

func verifyDigest(digest string, data []byte) error {
	if digest != digestOf(data) {
		return fmt.Errorf("snapshot checksum mismatch")
	}
	return nil
}

func digestOf(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// writeFileAtomic stages contents in a private temporary file, fsyncs it and
// atomically renames it over name. The rename is the commit point: callers
// must treat the new contents as durable after committed == true even when
// the subsequent directory fsync reports an error.
func writeFileAtomic(dir, name string, contents []byte) (committed bool, err error) {
	f, err := os.CreateTemp(dir, tempPrefix+"*")
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
	if err = os.Rename(temporary, filepath.Join(dir, name)); err != nil {
		return false, err
	}
	return true, syncDir(dir)
}

// sweepTempFiles removes leftovers of interrupted atomic writes. Only regular
// files carrying the temporary prefix are touched; anything else is left in
// place so that unexpected directory contents surface as a startup error.
func sweepTempFiles(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), tempPrefix) {
			continue
		}
		if err := os.Remove(filepath.Join(dir, entry.Name())); err != nil {
			return err
		}
	}
	return syncDir(dir)
}
