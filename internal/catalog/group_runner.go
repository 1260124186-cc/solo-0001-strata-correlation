package catalog

import (
	"context"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/correlation"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/geology"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/persistence"
	"log/slog"
	"sync"
	"time"
)

// 单进程内的对比结果计算容量与存储容量保持同一量级：
// 正在写入状态的计算最多并发 workers 个。
type flight struct {
	done   chan struct{}
	result correlation.Result
	reused bool
	err    error
}

type GroupRunner struct {
	service *Service
	logger  *slog.Logger
	workers int

	mu     sync.Mutex
	jobs   []job
	active map[string]map[int]bool // 组内已在调度的项，防止重复入队
	cond   *sync.Cond
	closed bool
	wg     sync.WaitGroup
}

type job struct {
	groupID string
	index   int
}

func New(repo *persistence.Repository, logger *slog.Logger, workers int) *Service {
	service := &Service{repo: repo, logger: logger}
	service.runner = &GroupRunner{service: service, logger: logger, workers: workers, active: map[string]map[int]bool{}}
	service.runner.cond = sync.NewCond(&service.runner.mu)
	return service
}

// Start 重置异常退出遗留的运行中状态，然后恢复调度器和工作协程，
// 并自动恢复所有尚未结束的对比组。
func (s *Service) Start() error {
	r := s.runner
	recovered := map[string][]int{}
	err := s.repo.Update(context.Background(), func(state *persistence.State) (bool, error) {
		if state.Groups == nil {
			state.Groups = map[string]correlation.Group{}
		}
		if state.Schema == 1 {
			state.Schema = 2
		}
		changed := false
		for id, group := range state.Groups {
			dirty := false
			for i := range group.Items {
				if group.Items[i].Status == correlation.ItemRunning {
					group.Items[i].Status = correlation.ItemQueued
					dirty = true
				}
			}
			if dirty {
				group.Status = group.DerivedStatus()
				group.UpdatedAt = time.Now().UTC()
				state.Groups[id] = group
				changed = true
			}
			if queued := group.QueuedIndexes(); len(queued) > 0 {
				recovered[id] = queued
			}
		}
		return changed, nil
	})
	if err != nil {
		return err
	}
	for id, indexes := range recovered {
		r.enqueue(id, indexes)
	}
	for i := 0; i < r.workers; i++ {
		r.wg.Add(1)
		go r.worker()
	}
	return nil
}

// Close 停止接收新任务，等待在处理的项完成（受优雅退出期限保护）。
func (s *Service) Close() {
	r := s.runner
	r.mu.Lock()
	r.closed = true
	r.cond.Broadcast()
	r.mu.Unlock()
	r.wg.Wait()
}

func (r *GroupRunner) worker() {
	defer r.wg.Done()
	for {
		groupID, index, ok := r.next()
		if !ok {
			return
		}
		r.process(context.Background(), groupID, index)
	}
}

func (r *GroupRunner) next() (string, int, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for len(r.jobs) == 0 && !r.closed {
		r.cond.Wait()
	}
	if len(r.jobs) == 0 {
		return "", 0, false
	}
	nextJob := r.jobs[0]
	r.jobs = r.jobs[1:]
	return nextJob.groupID, nextJob.index, true
}

// enqueue 幂等地把组内排队项加入调度；已在调度的项不会重复加入。
func (r *GroupRunner) enqueue(groupID string, indexes []int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	items := r.active[groupID]
	if items == nil {
		items = map[int]bool{}
		r.active[groupID] = items
	}
	for _, index := range indexes {
		if items[index] {
			continue
		}
		items[index] = true
		r.jobs = append(r.jobs, job{groupID: groupID, index: index})
	}
	r.cond.Broadcast()
}

func (r *GroupRunner) release(groupID string, index int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if items := r.active[groupID]; items != nil {
		delete(items, index)
		if len(items) == 0 {
			delete(r.active, groupID)
		}
	}
}

// compute 保证同一输入（含算法版本）在进程内只计算一次：复用已保存
// 结果、复用正在进行的计算，首个请求承担计算和持久化。
func (s *Service) compute(ctx context.Context, input correlation.Request) (correlation.Result, bool, error) {
	key := input.Key()
	s.flightMu.Lock()
	if s.flights == nil {
		s.flights = map[string]*flight{}
	}
	if ongoing, ok := s.flights[key]; ok {
		s.flightMu.Unlock()
		select {
		case <-ongoing.done:
			// 加入了别的请求发起的计算，对该请求而言属于复用。
			return ongoing.result, ongoing.err == nil, ongoing.err
		case <-ctx.Done():
			return correlation.Result{}, false, ctx.Err()
		}
	}
	// 先在飞行表占位，再检查已保存结果，消除“检查与注册”之间的竞争窗口。
	ongoing := &flight{done: make(chan struct{})}
	s.flights[key] = ongoing
	var cached correlation.Result
	found := false
	_ = s.repo.View(context.Background(), func(state persistence.State) error {
		if value, ok := state.Comparisons[key]; ok {
			cached = value.Clone()
			found = true
		}
		return nil
	})
	if found {
		delete(s.flights, key)
		s.flightMu.Unlock()
		return cached, true, nil
	}
	s.flightMu.Unlock()

	ongoing.result, ongoing.reused, ongoing.err = s.runCompute(input)
	close(ongoing.done)
	s.flightMu.Lock()
	delete(s.flights, key)
	s.flightMu.Unlock()
	return ongoing.result, ongoing.reused, ongoing.err
}

func (s *Service) runCompute(input correlation.Request) (correlation.Result, bool, error) {
	var cached correlation.Result
	found := false
	err := s.repo.View(context.Background(), func(state persistence.State) error {
		if value, ok := state.Comparisons[input.Key()]; ok {
			cached = value.Clone()
			found = true
		}
		return nil
	})
	if err != nil {
		return correlation.Result{}, false, err
	}
	if found {
		return cached, true, nil
	}
	now := time.Now().UTC()
	var leftRev, rightRev geology.Revision
	err = s.repo.View(context.Background(), func(state persistence.State) error {
		left, err := state.Revision(input.Left.ID, input.Left.Version)
		if err != nil {
			return err
		}
		right, err := state.Revision(input.Right.ID, input.Right.Version)
		if err != nil {
			return err
		}
		leftRev, rightRev = left, right
		return nil
	})
	if err != nil {
		return correlation.Result{}, false, err
	}
	// 区间合并不触碰共享状态，放在全局写入锁之外，配合 worker 上限形成并发限制。
	result, err := correlation.Align(leftRev.Profile, rightRev.Profile, input, now)
	if err != nil {
		return correlation.Result{}, false, err
	}
	reused := false
	err = s.repo.Update(context.Background(), func(state *persistence.State) (bool, error) {
		if existing, ok := state.Comparisons[result.ID]; ok {
			result = existing.Clone()
			reused = true
			return false, nil
		}
		if len(state.Comparisons) >= 10000 {
			return false, geology.Conflict("对比结果数量达到 10000 条上限")
		}
		state.Comparisons[result.ID] = result.Clone()
		return true, nil
	})
	return result, reused, err
}
