package persistence

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
)

var ErrReadOnly = errors.New("repository is mounted read-only")

type Repository struct {
	mu       sync.RWMutex
	reloadMu sync.Mutex
	state    State
	path     string
	lock     *os.File
	identity snapshotIdentity
	closed   bool
	fault    error
	readOnly bool
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
	repo, err := openSnapshot(absolute, lock)
	if err != nil {
		syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
		lock.Close()
		return nil, err
	}
	return repo, nil
}

func OpenReadOnly(dir string) (*Repository, error) {
	absolute, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("data path is not a directory: %s", absolute)
	}
	repo, err := openSnapshot(absolute, nil)
	if err != nil {
		return nil, err
	}
	repo.readOnly = true
	return repo, nil
}

func openSnapshot(dir string, lock *os.File) (*Repository, error) {
	path := filepath.Join(dir, "strata.json")
	state, identity, err := readSnapshot(path)
	if err != nil {
		return nil, err
	}
	return &Repository{
		state:    state,
		path:     path,
		lock:     lock,
		identity: identity,
	}, nil
}

func (r *Repository) View(ctx context.Context, fn func(State) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.RLock()
	if r.closed {
		r.mu.RUnlock()
		return fmt.Errorf("repository closed")
	}
	state := r.state
	identity := r.identity
	readOnly := r.readOnly
	r.mu.RUnlock()

	if readOnly {
		if err := r.refresh(identity); err != nil {
			return err
		}
		r.mu.RLock()
		if r.closed {
			r.mu.RUnlock()
			return fmt.Errorf("repository closed")
		}
		state = r.state
		r.mu.RUnlock()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return fn(state.Clone())
}

func (r *Repository) refresh(previous snapshotIdentity) error {
	r.reloadMu.Lock()
	defer r.reloadMu.Unlock()
	r.mu.RLock()
	current := r.identity
	closed := r.closed
	r.mu.RUnlock()
	if closed {
		return fmt.Errorf("repository closed")
	}
	if current != previous {
		return nil
	}

	info, err := os.Stat(r.path)
	if os.IsNotExist(err) {
		if previous == (snapshotIdentity{}) {
			return nil
		}
		return fmt.Errorf("committed snapshot disappeared")
	}
	if err != nil {
		return err
	}
	identity := snapshotIdentityFromInfo(info)
	if identity == current {
		return nil
	}

	state, loaded, err := readSnapshot(r.path)
	if err != nil {
		return err
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return fmt.Errorf("repository closed")
	}
	if r.identity != current {
		r.mu.Unlock()
		return nil
	}
	r.state = state
	r.identity = loaded
	r.mu.Unlock()
	return nil
}

func (r *Repository) Update(ctx context.Context, fn func(*State) (bool, error)) error {
	if r.readOnly {
		return ErrReadOnly
	}
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
	identity, committed, err := writeSnapshot(r.path, next)
	if committed {
		r.state = next
		if identity != (snapshotIdentity{}) {
			r.identity = identity
		}
	}
	if err != nil {
		if committed {
			r.fault = err
		}
		return fmt.Errorf("persist snapshot: %w", err)
	}
	return nil
}

func (r *Repository) Health() error {
	r.mu.RLock()
	closed := r.closed
	fault := r.fault
	readOnly := r.readOnly
	identity := r.identity
	r.mu.RUnlock()
	if closed {
		return fmt.Errorf("repository closed")
	}
	if readOnly {
		return r.refresh(identity)
	}
	return fault
}

func (r *Repository) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	r.closed = true
	if r.lock == nil {
		return nil
	}
	err := syscall.Flock(int(r.lock.Fd()), syscall.LOCK_UN)
	closeErr := r.lock.Close()
	if err != nil {
		return err
	}
	return closeErr
}
