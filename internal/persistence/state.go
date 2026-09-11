package persistence

import (
	"encoding/json"
	"fmt"
	"reflect"
	"time"

	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/correlation"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/geology"
)

// Branch 记录一条并行修订线从哪里分叉、当前头版本是什么。主线恒为 "main"。
type Branch struct {
	ID          string    `json:"id"`
	Name        string    `json:"name,omitempty"`
	ForkPoint   int       `json:"fork_point"`
	HeadVersion int       `json:"head_version"`
	CreatedAt   time.Time `json:"created_at,omitempty"`
}

type State struct {
	Schema      int                           `json:"schema"`
	Histories   map[string][]geology.Revision `json:"histories"`
	Branches    map[string][]Branch           `json:"branches"`
	Comparisons map[string]correlation.Result `json:"comparisons"`
}

func emptyState() State {
	return State{
		Schema:      2,
		Histories:   map[string][]geology.Revision{},
		Branches:    map[string][]Branch{},
		Comparisons: map[string]correlation.Result{},
	}
}

func (s State) Clone() State {
	out := emptyState()
	for id, revisions := range s.Histories {
		copies := make([]geology.Revision, len(revisions))
		for i, r := range revisions {
			copies[i] = r.Clone()
		}
		out.Histories[id] = copies
	}
	for id, branches := range s.Branches {
		copies := make([]Branch, len(branches))
		copy(copies, branches)
		out.Branches[id] = copies
	}
	for id, result := range s.Comparisons {
		out.Comparisons[id] = result.Clone()
	}
	return out
}

// revisionByVersion 按事件的真实版本号定位修订，绝不使用切片位置推断编号。
func revisionByVersion(history []geology.Revision, version int) (geology.Revision, bool) {
	for _, r := range history {
		if r.Event.Version == version {
			return r, true
		}
	}
	return geology.Revision{}, false
}

func (s State) historyOf(id string) ([]geology.Revision, error) {
	history, ok := s.Histories[id]
	if !ok || len(history) == 0 {
		return nil, geology.Missing("剖面不存在")
	}
	return history, nil
}

// Head 返回指定修订线的当前头版本；branch 为空时表示主线。
func (s State) Head(id, branch string) (geology.Profile, error) {
	revision, err := s.HeadRevision(id, branch)
	if err != nil {
		return geology.Profile{}, err
	}
	return revision.Profile.Clone(), nil
}

func (s State) LookupBranch(id, branch string) (*Branch, []Branch, error) {
	if branch == "" {
		branch = geology.MainBranch
	}
	branches, ok := s.Branches[id]
	if !ok {
		return nil, nil, geology.Missing("剖面不存在")
	}
	for i := range branches {
		if branches[i].ID == branch {
			copy := branches[i]
			return &copy, branches, nil
		}
	}
	return nil, nil, geology.Missing("修订线不存在")
}

func (s State) branchRecord(id, branch string) (*Branch, []Branch, error) {
	return s.LookupBranch(id, branch)
}

func (s State) HeadRevision(id, branch string) (geology.Revision, error) {
	history, err := s.historyOf(id)
	if err != nil {
		return geology.Revision{}, err
	}
	record, _, err := s.branchRecord(id, branch)
	if err != nil {
		return geology.Revision{}, err
	}
	revision, ok := revisionByVersion(history, record.HeadVersion)
	if !ok {
		return geology.Revision{}, geology.Missing("修订线头版本不存在")
	}
	return revision.Clone(), nil
}

// Latest 保持主线语义：不传修订线时读写主修订线。
func (s State) Latest(id string) (geology.Profile, error) {
	return s.Head(id, geology.MainBranch)
}

func (s State) Revision(id string, version int) (geology.Revision, error) {
	history, err := s.historyOf(id)
	if err != nil {
		return geology.Revision{}, err
	}
	if version < 1 {
		return geology.Revision{}, geology.Missing("历史版本不存在")
	}
	revision, ok := revisionByVersion(history, version)
	if !ok {
		return geology.Revision{}, geology.Missing("历史版本不存在")
	}
	return revision.Clone(), nil
}

func (s State) Validate() error {
	if s.Schema != 2 || s.Histories == nil || s.Branches == nil || s.Comparisons == nil {
		return fmt.Errorf("unsupported snapshot shape")
	}
	for id := range s.Histories {
		if err := s.validateHistory(id, s.Histories[id]); err != nil {
			return err
		}
	}
	for id, result := range s.Comparisons {
		if id != result.ID || id != result.Request.Key() || result.Algorithm != correlation.Algorithm || result.CreatedAt.IsZero() {
			return fmt.Errorf("invalid comparison identity")
		}
		a, err := s.Revision(result.Request.Left.ID, result.Request.Left.Version)
		if err != nil {
			return err
		}
		b, err := s.Revision(result.Request.Right.ID, result.Request.Right.Version)
		if err != nil {
			return err
		}
		computed, err := correlation.Align(a.Profile, b.Profile, result.Request, result.CreatedAt)
		if err != nil {
			return err
		}
		expected, _ := json.Marshal(computed)
		actual, _ := json.Marshal(result)
		if string(expected) != string(actual) {
			return fmt.Errorf("comparison data mismatch")
		}
	}
	return nil
}

func (s State) validateHistory(id string, history []geology.Revision) error {
	if len(history) == 0 {
		return fmt.Errorf("empty history %s", id)
	}
	branches := s.Branches[id]
	if len(branches) == 0 {
		return fmt.Errorf("missing branch registry %s", id)
	}
	byVersion := make(map[int]geology.Revision, len(history))
	versions := make(map[int]bool)
	mainID := geology.MainBranch
	registered := map[string]*Branch{}
	var hasMain bool
	for i := range branches {
		b := &branches[i]
		if !geology.ValidBranch(b.ID) {
			return fmt.Errorf("invalid branch id %s/%s", id, b.ID)
		}
		if b.ID == mainID {
			if hasMain {
				return fmt.Errorf("duplicate branch %s/%s", id, b.ID)
			}
			hasMain = true
		} else if _, dup := registered[b.ID]; dup {
			return fmt.Errorf("duplicate branch %s/%s", id, b.ID)
		}
		registered[b.ID] = b
	}
	if !hasMain {
		return fmt.Errorf("missing main branch %s", id)
	}
	for i, r := range history {
		if r.Profile.ID != id {
			return fmt.Errorf("inconsistent revision %s/%d", id, r.Event.Version)
		}
		if versions[r.Event.Version] {
			return fmt.Errorf("duplicate version %s/%d", id, r.Event.Version)
		}
		versions[r.Event.Version] = true
		if r.Profile.Version != r.Event.Version {
			return fmt.Errorf("inconsistent revision %s/%d", id, r.Event.Version)
		}
		if !r.Event.At.Equal(r.Profile.UpdatedAt) {
			return fmt.Errorf("inconsistent revision %s/%d", id, r.Event.Version)
		}
		if err := r.Profile.Validate(); err != nil {
			return fmt.Errorf("invalid revision %s: %w", id, err)
		}
		if err := geology.Text("reason", r.Event.Reason, 1, 500); err != nil {
			return err
		}
		branch := r.Event.Branch
		if branch == "" {
			branch = mainID
		}
		if r.Profile.Branch != branch {
			return fmt.Errorf("revision branch mismatch %s/%d", id, r.Event.Version)
		}
		if _, ok := registered[branch]; !ok {
			return fmt.Errorf("unregistered branch %s/%s", id, branch)
		}
		byVersion[r.Event.Version] = r
		if i > 0 && r.Event.At.Before(history[i-1].Event.At) {
			return fmt.Errorf("invalid revision chronology %s", id)
		}
	}
	// 版本号必须从 1 开始稠密分配，但相邻版本不再被假定为内容上的前后继。
	for v := 1; v <= len(history); v++ {
		if !versions[v] {
			return fmt.Errorf("non-contiguous version numbering %s", id)
		}
	}
	first := history[0]
	if first.Event.Action != "create" || first.Profile.State != geology.Draft ||
		first.Event.Version != 1 || (first.Event.Branch != "" && first.Event.Branch != mainID) {
		return fmt.Errorf("invalid initial revision %s", id)
	}
	for _, r := range history[1:] {
		if r.Event.Action != "fork" {
			parent, ok := byVersion[r.Event.Parent]
			if !ok {
				return fmt.Errorf("revision %s/%d missing parent %d", id, r.Event.Version, r.Event.Parent)
			}
			if err := validateStep(parent, r); err != nil {
				return fmt.Errorf("invalid step %s/%d: %w", id, r.Event.Version, err)
			}
			// 引入分叉后版本号全局分配，父版本可以不是前一个版本；
			// 但父版本必须更旧（分叉事件通过 ForkPoint 单独校验）。
			if r.Event.Parent >= r.Event.Version {
				return fmt.Errorf("revision %s/%d parent not older", id, r.Event.Version)
			}
		} else {
			if err := validateStep(byVersion[r.Event.ForkPoint], r); err != nil {
				return fmt.Errorf("invalid fork step %s/%d: %w", id, r.Event.Version, err)
			}
		}
		switch r.Event.Action {
		case "fork":
			if err := validateFork(registered, byVersion, r); err != nil {
				return fmt.Errorf("invalid fork %s/%d: %w", id, r.Event.Version, err)
			}
		case "merge":
			if err := validateMerge(registered, byVersion, r); err != nil {
				return fmt.Errorf("invalid merge %s/%d: %w", id, r.Event.Version, err)
			}
		}
	}
	// 每条登记线的头必须真实存在，且头版本必须属于该修订线。
	for _, b := range branches {
		head, ok := byVersion[b.HeadVersion]
		if !ok {
			return fmt.Errorf("branch %s/%s points at missing head %d", id, b.ID, b.HeadVersion)
		}
		if head.Event.Branch != b.ID {
			return fmt.Errorf("branch %s/%s head belongs to %s", id, b.ID, head.Event.Branch)
		}
		if b.ID != mainID {
			if _, exists := byVersion[b.ForkPoint]; !exists {
				return fmt.Errorf("branch %s/%s fork point %d missing", id, b.ID, b.ForkPoint)
			}
			if fork := forkEventFor(byVersion, b.ID); fork == nil || fork.Event.ForkPoint != b.ForkPoint ||
				fork.Event.Version != firstOnBranch(byVersion, b.ID) {
				return fmt.Errorf("branch %s/%s fork event mismatch", id, b.ID)
			}
		}
	}
	return nil
}

func forkEventFor(byVersion map[int]geology.Revision, branch string) *geology.Revision {
	for _, r := range byVersion {
		if r.Event.Action == "fork" && r.Event.Branch == branch {
			copy := r
			return &copy
		}
	}
	return nil
}

func firstOnBranch(byVersion map[int]geology.Revision, branch string) int {
	first := 0
	for v, r := range byVersion {
		if r.Event.Branch == branch && (first == 0 || v < first) {
			first = v
		}
	}
	return first
}

func validateFork(registered map[string]*Branch, byVersion map[int]geology.Revision, r geology.Revision) error {
	branch := r.Event.Branch
	if branch == geology.MainBranch {
		return fmt.Errorf("cannot fork onto main")
	}
	record, ok := registered[branch]
	if !ok || record == nil {
		return fmt.Errorf("fork creates unregistered branch")
	}
	if record.ForkPoint != r.Event.ForkPoint || r.Event.Version != firstOnBranch(byVersion, branch) {
		return fmt.Errorf("fork point does not match registry")
	}
	if _, exists := byVersion[r.Event.ForkPoint]; !exists {
		return fmt.Errorf("fork point missing")
	}
	if r.Event.Parent != 0 {
		return fmt.Errorf("fork revision must not declare content parent")
	}
	if r.Event.Merge != nil {
		return fmt.Errorf("fork must not carry merge ref")
	}
	if r.Profile.State != geology.Draft {
		return fmt.Errorf("forked line must reopen as draft")
	}
	return nil
}

func validateMerge(registered map[string]*Branch, byVersion map[int]geology.Revision, r geology.Revision) error {
	if r.Event.Branch != geology.MainBranch {
		return fmt.Errorf("merge must land on main")
	}
	merge := r.Event.Merge
	if merge == nil {
		return fmt.Errorf("merge event missing source ref")
	}
	if _, ok := registered[merge.Source]; !ok {
		return fmt.Errorf("merge source branch not registered")
	}
	source, ok := byVersion[merge.SourceVersion]
	if !ok || source.Event.Branch != merge.Source {
		return fmt.Errorf("merge source version mismatch")
	}
	if _, ok := byVersion[merge.BaseVersion]; !ok {
		return fmt.Errorf("merge base missing")
	}
	if r.Profile.State != geology.Draft {
		return fmt.Errorf("merge must produce draft")
	}
	return nil
}

func validateStep(before geology.Revision, r geology.Revision) error {
	after := r.Profile
	if before.Profile.CreatedAt != after.CreatedAt {
		return fmt.Errorf("created_at changed")
	}
	switch r.Event.Action {
	case "metadata", "layers":
		if before.Profile.Branch != after.Branch || r.Event.Branch != after.Branch {
			return fmt.Errorf("branch changed on edit")
		}
		if before.Profile.State != geology.Draft || after.State != geology.Draft {
			return fmt.Errorf("edited sealed revision")
		}
		if r.Event.Action == "layers" && before.Profile.Metadata != after.Metadata {
			return fmt.Errorf("layers edit changed metadata")
		}
		if r.Event.Action == "metadata" && !reflect.DeepEqual(before.Profile.Layers, after.Layers) {
			return fmt.Errorf("metadata edit changed layers")
		}
	case "seal", "reopen":
		if before.Profile.Branch != after.Branch {
			return fmt.Errorf("branch changed on state change")
		}
		expected := geology.Sealed
		if r.Event.Action == "reopen" {
			expected = geology.Draft
		}
		if after.State != expected || before.Profile.State == expected || before.Profile.Metadata != after.Metadata || !reflect.DeepEqual(before.Profile.Layers, after.Layers) {
			return fmt.Errorf("invalid state change")
		}
	case "fork":
		if after.Branch != r.Event.Branch || after.State != geology.Draft {
			return fmt.Errorf("invalid fork state")
		}
		if before.Profile.ID != after.ID {
			return fmt.Errorf("fork crossed profiles")
		}
	case "merge":
		if after.Branch != geology.MainBranch || after.State != geology.Draft {
			return fmt.Errorf("invalid merge state")
		}
	default:
		return fmt.Errorf("unknown revision action")
	}
	return nil
}
