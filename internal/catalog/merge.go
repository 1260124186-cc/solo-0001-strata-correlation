package catalog

import (
	"context"
	"strings"

	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/geology"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/persistence"
)

// lineage 提供按真实版本事件遍历修订 DAG 的能力，绝不依赖切片位置。
type lineage struct {
	byVersion map[int]geology.Revision
}

func newLineage(history []geology.Revision) lineage {
	out := lineage{byVersion: make(map[int]geology.Revision, len(history))}
	for _, r := range history {
		out.byVersion[r.Event.Version] = r
	}
	return out
}

// ancestors 返回某版本沿内容来源可达的全部祖先版本集合。
// 合并提交同时追溯第一父（主线）与合并来源（分叉线头），从而合并带入的版本
// 对主线头同样可达。
func (l lineage) ancestors(version int) map[int]bool {
	seen := map[int]bool{}
	stack := []int{version}
	for len(stack) > 0 {
		v := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if seen[v] {
			continue
		}
		seen[v] = true
		r, ok := l.byVersion[v]
		if !ok {
			continue
		}
		switch {
		case r.Event.Action == "fork":
			stack = append(stack, r.Event.ForkPoint)
		default:
			if r.Event.Parent > 0 {
				stack = append(stack, r.Event.Parent)
			}
			if r.Event.Merge != nil {
				stack = append(stack, r.Event.Merge.SourceVersion)
			}
		}
	}
	return seen
}

// mainChain 返回主线从头版本向第一父回退的版本序列（新版本在前）。
func (l lineage) mainChain(mainHead int) []int {
	chain := []int{}
	for v := mainHead; v > 0; {
		r, ok := l.byVersion[v]
		if !ok {
			break
		}
		chain = append(chain, v)
		switch {
		case r.Event.Action == "fork":
			v = r.Event.ForkPoint
		case r.Event.Parent > 0:
			v = r.Event.Parent
		default:
			v = 0
		}
	}
	return chain
}

// MergePreviewRequest 是合并前逐项差异的输入。
type MergePreviewRequest struct {
	Source string `json:"source"`
}

// MergeRequest 把分叉线合并回主线，必须携带主线头预期版本。
type MergeRequest struct {
	Source                string `json:"source"`
	ExpectedVersion       int    `json:"expected_version"`
	ExpectedSourceVersion int    `json:"expected_source_version"`
	Reason                string `json:"reason"`
}

// MergePreview 给出主线/分叉线相对基点的逐项差异、冲突以及可合并结果。
type MergePreview struct {
	Source        string              `json:"source"`
	BaseVersion   int                 `json:"base_version"`
	MainVersion   int                 `json:"main_version"`
	SourceVersion int                 `json:"source_version"`
	Ahead         int                 `json:"ahead"`
	Behind        int                 `json:"behind"`
	Mergeable     bool                `json:"mergeable"`
	Report        geology.MergeReport `json:"report"`
}

// MergeResult 是执行合并后的结果：新主线版本、来源版本仍可读。
type MergeResult struct {
	Preview  MergePreview     `json:"preview"`
	Revision geology.Revision `json:"revision"`
}

// mergeBase 返回主线头与来源头的最近共同版本。合并提交同时把来源版本并入
// 主线可达集合，因此上一次合并的来源版本会成为新基点（再次合并场景）。
func (l lineage) mergeBase(mainHead int, sourceHead int) int {
	mainAncestors := l.ancestors(mainHead)
	sourceAncestors := l.ancestors(sourceHead)
	best := 0
	for v := range sourceAncestors {
		if mainAncestors[v] && v > best {
			best = v
		}
	}
	return best
}

func (s *Service) Relation(ctx context.Context, id, branch string) (BranchView, error) {
	branch, err := resolveBranch(branch)
	if err != nil {
		return BranchView{}, err
	}
	var result BranchView
	err = s.repo.View(ctx, func(state persistence.State) error {
		views, err := branchViews(state, id)
		if err != nil {
			return err
		}
		var view *BranchView
		for i := range views {
			if views[i].ID == branch {
				view = &views[i]
				break
			}
		}
		if view == nil {
			return geology.Missing("修订线不存在")
		}
		if err = fillRelation(state, id, view); err != nil {
			return err
		}
		result = *view
		return nil
	})
	return result, err
}

func fillRelation(state persistence.State, id string, view *BranchView) error {
	history := state.Histories[id]
	line := newLineage(history)
	mainHead, err := state.HeadRevision(id, geology.MainBranch)
	if err != nil {
		return err
	}
	if view.IsMain {
		view.MergeBase = mainHead.Event.Version
		return nil
	}
	sourceHead, err := state.HeadRevision(id, view.ID)
	if err != nil {
		return err
	}
	base := line.mergeBase(mainHead.Event.Version, sourceHead.Event.Version)
	view.MergeBase = base
	baseAncestors := line.ancestors(base)
	// ahead：来源线上基点之后的版本（沿来源第一父链，直到进入基点祖先）。
	for _, v := range lineagePath(line, sourceHead.Event.Version) {
		if baseAncestors[v] {
			break
		}
		view.Ahead++
	}
	// behind：主线上基点之后的版本（含合并提交），按主线第一父链回退计数。
	for _, v := range line.mainChain(mainHead.Event.Version) {
		if v == base || baseAncestors[v] {
			break
		}
		view.Behind++
	}
	// 基点推进到分叉点之后，说明来源内容已经通过某次合并进入主线。
	view.Merged = base != view.ForkPoint
	view.SourceSealed = sourceHead.Profile.State == geology.Sealed
	view.HeadVersion = sourceHead.Event.Version
	return nil
}

// lineagePath 返回从某版本沿第一父回到根的版本序列。
func lineagePath(line lineage, version int) []int {
	path := []int{}
	for v := version; v > 0; {
		path = append(path, v)
		r, ok := line.byVersion[v]
		if !ok {
			break
		}
		switch {
		case r.Event.Action == "fork":
			v = r.Event.ForkPoint
		case r.Event.Parent > 0:
			v = r.Event.Parent
		default:
			v = 0
		}
	}
	return path
}

func (s *Service) PreviewMerge(ctx context.Context, id string, input MergePreviewRequest) (MergePreview, error) {
	source, err := resolveBranch(strings.TrimSpace(input.Source))
	if err != nil {
		return MergePreview{}, err
	}
	if source == geology.MainBranch {
		return MergePreview{}, geology.Invalid("source", "来源必须是分叉修订线，不能是主线")
	}
	var preview MergePreview
	err = s.repo.View(ctx, func(state persistence.State) error {
		p, err := buildPreview(state, id, source)
		if err != nil {
			return err
		}
		preview = p
		return nil
	})
	return preview, err
}

func buildPreview(state persistence.State, id, source string) (MergePreview, error) {
	history := state.Histories[id]
	if history == nil {
		return MergePreview{}, geology.Missing("剖面不存在")
	}
	if _, _, err := state.LookupBranch(id, source); err != nil {
		return MergePreview{}, err
	}
	mainHead, err := state.HeadRevision(id, geology.MainBranch)
	if err != nil {
		return MergePreview{}, err
	}
	sourceHead, err := state.HeadRevision(id, source)
	if err != nil {
		return MergePreview{}, err
	}
	line := newLineage(history)
	baseVersion := line.mergeBase(mainHead.Event.Version, sourceHead.Event.Version)
	if baseVersion == 0 {
		return MergePreview{}, geology.Conflict("找不到分叉线与主线的共同基点")
	}
	base, ok := revisionByVersion(history, baseVersion)
	if !ok {
		return MergePreview{}, geology.Missing("合并基点版本不存在")
	}
	report, err := geology.ThreeWayMerge(base.Profile, mainHead.Profile, sourceHead.Profile)
	if err != nil {
		return MergePreview{}, err
	}
	preview := MergePreview{
		Source: source, BaseVersion: baseVersion,
		MainVersion: mainHead.Event.Version, SourceVersion: sourceHead.Event.Version,
		Mergeable: !report.HasConflict(), Report: report,
	}
	baseAncestors := line.ancestors(baseVersion)
	for _, v := range lineagePath(line, sourceHead.Event.Version) {
		if baseAncestors[v] {
			break
		}
		preview.Ahead++
	}
	for _, v := range line.mainChain(mainHead.Event.Version) {
		if v == baseVersion {
			break
		}
		preview.Behind++
	}
	return preview, nil
}

func (s *Service) Merge(ctx context.Context, id string, input MergeRequest) (MergeResult, error) {
	source, err := resolveBranch(strings.TrimSpace(input.Source))
	if err != nil {
		return MergeResult{}, err
	}
	if source == geology.MainBranch {
		return MergeResult{}, geology.Invalid("source", "来源必须是分叉修订线，不能是主线")
	}
	reason, err := normalizedReason(input.Reason)
	if err != nil {
		return MergeResult{}, err
	}
	var result MergeResult
	err = s.repo.Update(ctx, func(state *persistence.State) (bool, error) {
		history := state.Histories[id]
		if history == nil {
			return false, geology.Missing("剖面不存在")
		}
		mainHead, err := state.HeadRevision(id, geology.MainBranch)
		if err != nil {
			return false, err
		}
		if mainHead.Profile.Version != input.ExpectedVersion {
			return false, geology.VersionConflict(input.ExpectedVersion, mainHead.Profile.Version)
		}
		if mainHead.Profile.State != geology.Draft {
			return false, geology.Conflict("主线当前版本已锁定，请先重新打开再合并")
		}
		sourceHead, err := state.HeadRevision(id, source)
		if err != nil {
			return false, err
		}
		if input.ExpectedSourceVersion != 0 && sourceHead.Profile.Version != input.ExpectedSourceVersion {
			return false, geology.VersionConflict(input.ExpectedSourceVersion, sourceHead.Profile.Version)
		}
		preview, err := buildPreview(*state, id, source)
		if err != nil {
			return false, err
		}
		if !preview.Mergeable {
			return false, geology.Conflict("分叉线与主线存在无法自动合并的冲突，请先逐项处理")
		}
		version, err := nextVersion(history)
		if err != nil {
			return false, err
		}
		merged := preview.Report.Merged.Clone()
		merged.ID = id
		merged.Version = version
		merged.Branch = geology.MainBranch
		now := nextTime(latestEventTime(history))
		merged.UpdatedAt = now
		event := geology.Event{
			Action: "merge", Reason: reason, Version: version, At: now,
			Branch: geology.MainBranch, Parent: mainHead.Event.Version,
			Merge: &geology.MergeRef{
				Source: source, SourceVersion: sourceHead.Event.Version, BaseVersion: preview.BaseVersion,
			},
		}
		revision := geology.Revision{Profile: merged, Event: event}
		if err = commitRevision(state, revision, geology.MainBranch); err != nil {
			return false, err
		}
		// 合并不删除分叉线：其头版本保持原样可读，且仍可继续编录后再次合并。
		preview.Mergeable = true
		result = MergeResult{Preview: preview, Revision: revision}
		return true, nil
	})
	return result, err
}
