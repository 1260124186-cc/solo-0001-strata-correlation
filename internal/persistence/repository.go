package persistence

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"syscall"

	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/geology"
)

type Repository struct {
	mu       sync.RWMutex
	state    State
	dir      string
	shards   string
	snapshot string
	lock     *os.File
	closed   bool
	// fault is set exactly when disk and memory may disagree: an atomic
	// rename landed but the directory fsync failed, or a later shard in the
	// same update failed after one already committed. Until restart every
	// further write is refused and the health endpoint reports 503.
	fault error
}

func Open(dir string) (*Repository, error) {
	absolute, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(absolute, 0700); err != nil {
		return nil, err
	}
	lock, err := os.OpenFile(filepath.Join(absolute, "strata.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lock.Close()
		return nil, fmt.Errorf("data directory is already in use: %w", err)
	}
	r := &Repository{
		dir:      absolute,
		shards:   filepath.Join(absolute, shardsDir),
		snapshot: filepath.Join(absolute, "strata.json"),
		lock:     lock,
	}
	state, err := r.bootstrap()
	if err != nil {
		syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
		_ = lock.Close()
		return nil, err
	}
	r.state = state
	return r, nil
}

// bootstrap makes the on-disk shape well-defined before any state is served:
// leftovers of interrupted writes are swept, a legacy whole snapshot is
// migrated into shards (idempotently), and the remaining shard set is loaded
// and cross-validated as one state.
func (r *Repository) bootstrap() (State, error) {
	if err := sweepTempFiles(r.dir); err != nil {
		return State{}, err
	}
	shardEntry, err := os.Stat(r.shards)
	switch {
	case err == nil:
		if !shardEntry.IsDir() {
			return State{}, fmt.Errorf("%s must be a directory", shardsDir)
		}
	case os.IsNotExist(err):
		if err = os.Mkdir(r.shards, 0700); err != nil {
			return State{}, err
		}
		if err = syncDir(r.dir); err != nil {
			return State{}, err
		}
	default:
		return State{}, err
	}
	if err = sweepTempFiles(r.shards); err != nil {
		return State{}, err
	}
	if _, statErr := os.Stat(r.snapshot); statErr == nil {
		legacy, readErr := readSnapshot(r.snapshot)
		if readErr != nil {
			return State{}, readErr
		}
		if migrateErr := migrateSnapshot(r.shards, r.snapshot, legacy); migrateErr != nil {
			return State{}, migrateErr
		}
	} else if !os.IsNotExist(statErr) {
		return State{}, statErr
	}
	return loadShards(r.shards)
}

func (r *Repository) View(ctx context.Context, fn func(State) error) error {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.closed {
		return fmt.Errorf("repository closed")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return fn(r.state.Clone())
}

func (r *Repository) Update(ctx context.Context, fn func(*State) (bool, error)) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return fmt.Errorf("repository closed")
	}
	if r.fault != nil {
		return fmt.Errorf("repository requires restart: %w", r.fault)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	next := r.state.Clone()
	changed, err := fn(&next)
	if err != nil {
		return err
	}
	if !changed {
		return nil
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = r.commit(next); err != nil {
		return err
	}
	return nil
}

// commit persists only the shards that differ from the in-memory state.
// Shards commit one at a time; a crash between them recovers deterministically
// on restart from whatever landed. Memory only advances after every changed
// shard is durable, so on a mid-transaction I/O failure reads still serve the
// last consistent state — while fault is set (a shard may have landed) and the
// health endpoint reports the memory/disk divergence as 503 until restart.
func (r *Repository) commit(next State) error {
	committed := 0
	fail := func(id string, err error) error {
		if committed > 0 {
			r.fault = fmt.Errorf("partial shard commit: %d shard(s) durable before failure", committed)
		}
		return fmt.Errorf("persist shard %s: %w", id, err)
	}

	var ids []string
	for id, history := range next.Histories {
		current, exists := r.state.Histories[id]
		if !exists || !reflect.DeepEqual(current, history) {
			ids = append(ids, id)
		}
	}
	for _, id := range ids {
		contents, err := encodeProfileShard(id, next.Histories[id])
		if err != nil {
			return fail(id, err)
		}
		durable, err := writeFileAtomic(r.shards, shardName(id), contents)
		if durable {
			committed++
		}
		if err != nil {
			return fail(id, err)
		}
	}

	var cmpIDs []string
	for id, result := range next.Comparisons {
		current, exists := r.state.Comparisons[id]
		if !exists || !reflect.DeepEqual(current, result) {
			cmpIDs = append(cmpIDs, id)
		}
	}
	for _, id := range cmpIDs {
		contents, err := encodeComparisonShard(next.Comparisons[id])
		if err != nil {
			return fail(id, err)
		}
		durable, err := writeFileAtomic(r.shards, shardName(id), contents)
		if durable {
			committed++
		}
		if err != nil {
			return fail(id, err)
		}
	}

	// Every changed shard is durable; make the in-memory copy match disk.
	// Deep-copy revisions out of next so later clones cannot share backing
	// arrays with the throwaway state passed to the mutator.
	for id, history := range next.Histories {
		copies := make([]geology.Revision, len(history))
		for i, revision := range history {
			copies[i] = revision.Clone()
		}
		r.state.Histories[id] = copies
	}
	for id, result := range next.Comparisons {
		r.state.Comparisons[id] = result.Clone()
	}
	return nil
}

func (r *Repository) Health() error {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.closed {
		return fmt.Errorf("repository closed")
	}
	return r.fault
}

func (r *Repository) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	r.closed = true
	err := syscall.Flock(int(r.lock.Fd()), syscall.LOCK_UN)
	closeErr := r.lock.Close()
	if err != nil {
		return err
	}
	return closeErr
}
