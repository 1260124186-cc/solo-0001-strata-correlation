package persistence

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

type Repository struct {
	mu     sync.RWMutex
	state  State
	path   string
	lock   *os.File
	closed bool
	fault  error
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
	path := filepath.Join(absolute, "strata.json")
	state, err := readSnapshot(path)
	if err != nil {
		syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
		lock.Close()
		return nil, err
	}
	// 崩溃可能发生在批次条目之间。running 状态在进程之外没有意义：
	// 已提交的条目保持原样，尚未执行的条目落为 not_attempted，批次
	// 据此终结为 completed 或 interrupted，使重启后不存在无法解释
	// 的半批状态。恢复结果必须落盘后才对外服务。
	if changed := reconcileBatches(&state, time.Now().UTC()); changed {
		if _, err = writeSnapshot(path, state); err != nil {
			syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
			lock.Close()
			return nil, fmt.Errorf("persist interrupted-batch recovery: %w", err)
		}
	}
	return &Repository{state: state, path: path, lock: lock}, nil
}

// reconcileBatches 终结所有遗留的 running 批次：没有 pending 条目则
// 视为完成，否则把 pending 条目标记为未执行并将批次置为 interrupted。
// 所有批次使用同一个恢复时刻。
func reconcileBatches(state *State, now time.Time) bool {
	changed := false
	for id, batch := range state.Batches {
		if batch.Status != BatchRunning {
			continue
		}
		pending := false
		for i := range batch.Items {
			if batch.Items[i].Status != ItemPending {
				continue
			}
			pending = true
			batch.Items[i].Status = ItemNotAttempted
			batch.Items[i].ErrorCode = "interrupted"
			batch.Items[i].Error = "服务在该条目执行前中断，修订未写入"
		}
		if pending {
			batch.Status = BatchInterrupted
		} else {
			batch.Status = BatchCompleted
		}
		batch.UpdatedAt = tick(batch.UpdatedAt, now)
		state.Batches[id] = batch
		changed = true
	}
	return changed
}

func tick(previous, now time.Time) time.Time {
	if now.Before(previous) {
		return previous
	}
	return now
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

// Commit 在单个快照提交点内应用 fn。返回的 committed 表示重命名是否
// 已经完成：committed=true 时 fn 的全部效果（包括它记录的失败）都已
// 落盘，即使随后同步目录报错，调用方也必须按已提交处理。
func (r *Repository) Commit(ctx context.Context, fn func(*State) (bool, error)) (committed bool, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return false, fmt.Errorf("repository closed")
	}
	if r.fault != nil {
		return false, fmt.Errorf("repository requires restart: %w", r.fault)
	}
	if err = ctx.Err(); err != nil {
		return false, err
	}
	next := r.state.Clone()
	changed, err := fn(&next)
	if err != nil {
		return false, err
	}
	if !changed {
		return false, nil
	}
	if err = ctx.Err(); err != nil {
		return false, err
	}
	committed, err = writeSnapshot(r.path, next)
	if committed {
		r.state = next
	}
	if err != nil {
		if committed {
			r.fault = err
		}
		return committed, fmt.Errorf("persist snapshot: %w", err)
	}
	return true, nil
}

func (r *Repository) Update(ctx context.Context, fn func(*State) (bool, error)) error {
	_, err := r.Commit(ctx, fn)
	return err
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
