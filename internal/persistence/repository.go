package persistence

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
)

type Repository struct {
	mu     sync.RWMutex
	state  State
	path   string
	lock   *os.File
	closed bool
	fault  error
}

// CommitError 报告快照保存失败。Committed 为 true 表示新状态已越过提交点
// （原子替换完成），对应修改必须视为已生效；为 false 表示内存与磁盘均未改变。
type CommitError struct {
	Committed bool
	Err       error
}

func (e *CommitError) Error() string { return "persist snapshot: " + e.Err.Error() }
func (e *CommitError) Unwrap() error { return e.Err }

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
	path := filepath.Join(absolute, "strata.json")
	state, err := readSnapshot(path)
	if err != nil {
		syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
		lock.Close()
		return nil, err
	}
	return &Repository{state: state, path: path, lock: lock}, nil
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
	committed, err := writeSnapshot(r.path, next)
	if committed {
		r.state = next
	}
	if err != nil {
		if committed {
			r.fault = err
		}
		return &CommitError{Committed: committed, Err: err}
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
