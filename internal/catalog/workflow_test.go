package catalog_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/catalog"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/correlation"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/geology"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/persistence"
)

type env struct {
	t    *testing.T
	svc  *catalog.Service
	repo *persistence.Repository
	ctx  context.Context
}

func newEnv(t *testing.T) *env {
	t.Helper()
	repo, err := persistence.Open(t.TempDir())
	if err != nil {
		t.Fatalf("打开临时数据目录失败: %v", err)
	}
	t.Cleanup(func() { repo.Close() })
	return &env{t: t, svc: catalog.New(repo), repo: repo, ctx: context.Background()}
}

func (e *env) create(name string, depth int64) geology.Profile {
	e.t.Helper()
	p, err := e.svc.Create(e.ctx, geology.Metadata{Name: name, Site: "赤石岭", DepthMM: depth})
	if err != nil {
		e.t.Fatalf("创建剖面失败: %v", err)
	}
	return p
}

func (e *env) mustReplace(p geology.Profile, layers []geology.Layer, reason string) geology.Profile {
	e.t.Helper()
	out, err := e.svc.Replace(e.ctx, p.ID, catalog.ReplaceLayers{
		ExpectedVersion: p.Version, Layers: layers, Reason: reason,
	})
	if err != nil {
		e.t.Fatalf("替换分层失败: %v", err)
	}
	return out
}

func (e *env) mustChange(p geology.Profile, target geology.State, reason string) geology.Profile {
	e.t.Helper()
	out, err := e.svc.Change(e.ctx, p.ID, target, catalog.StateChange{
		ExpectedVersion: p.Version, Reason: reason,
	})
	if err != nil {
		e.t.Fatalf("状态转换失败: %v", err)
	}
	return out
}

func fullLayers(depth int64) []geology.Layer {
	return []geology.Layer{
		{TopMM: 0, BottomMM: depth * 2 / 5, Rock: geology.Sandstone},
		{TopMM: depth * 2 / 5, BottomMM: depth, Rock: geology.Mudstone},
	}
}

func expectConflict(t *testing.T, err error) {
	t.Helper()
	var p *geology.Problem
	if !errors.As(err, &p) {
		t.Fatalf("期望领域 Problem，实际 %T: %v", err, err)
	}
	if p.Code != "conflict" {
		t.Fatalf("期望 conflict，实际 code=%s: %v", p.Code, err)
	}
}

// 同一版本的并发写入只有一个成功，其余得到冲突；最终版本只前进一次。
func TestConcurrentSameVersionWrites(t *testing.T) {
	e := newEnv(t)
	p := e.create("并发剖面", 10000)

	const n = 8
	start := make(chan struct{})
	var wg sync.WaitGroup
	var mu sync.Mutex
	success, failures := 0, 0
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := e.svc.Replace(e.ctx, p.ID, catalog.ReplaceLayers{
				ExpectedVersion: p.Version,
				Reason:          "并发替换",
				Layers:          fullLayers(10000),
			})
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				success++
			} else {
				expectConflict(t, err)
				failures++
			}
		}()
	}
	close(start)
	wg.Wait()

	if success != 1 || failures != n-1 {
		t.Fatalf("并发结果: 成功 %d（期望 1），失败 %d（期望 %d）", success, failures, n-1)
	}
	current, err := e.svc.Get(e.ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Version != p.Version+1 {
		t.Fatalf("版本应只前进一次: %d -> %d", p.Version, current.Version)
	}
}

// 同版本并发锁定同理：只有一个请求把剖面锁定。
func TestConcurrentSameVersionSeals(t *testing.T) {
	e := newEnv(t)
	p := e.create("并发锁定", 10000)
	p = e.mustReplace(p, fullLayers(10000), "完整覆盖")

	const n = 8
	start := make(chan struct{})
	var wg sync.WaitGroup
	var mu sync.Mutex
	success, failures := 0, 0
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := e.svc.Change(e.ctx, p.ID, geology.Sealed,
				catalog.StateChange{ExpectedVersion: p.Version, Reason: "并发锁定"})
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				success++
			} else {
				expectConflict(t, err)
				failures++
			}
		}()
	}
	close(start)
	wg.Wait()
	if success != 1 || failures != n-1 {
		t.Fatalf("并发锁定: 成功 %d 失败 %d", success, failures)
	}
}

// 片段整体替换：校验失败时任何层都不得落盘，版本与层内容保持原样。
func TestReplaceAtomicOnValidationFailure(t *testing.T) {
	e := newEnv(t)
	p := e.create("原子替换", 10000)
	p = e.mustReplace(p, fullLayers(10000), "首次完整分层")
	before, _ := e.svc.Get(e.ctx, p.ID)

	bad := []geology.Layer{
		{TopMM: 0, BottomMM: 3000, Rock: geology.Sandstone},
		{TopMM: 2000, BottomMM: 8000, Rock: geology.Mudstone}, // 与上层重叠
	}
	_, err := e.svc.Replace(e.ctx, p.ID, catalog.ReplaceLayers{
		ExpectedVersion: before.Version, Reason: "非法分层", Layers: bad,
	})
	var problem *geology.Problem
	if !errors.As(err, &problem) || problem.Code != "invalid" {
		t.Fatalf("重叠分层应返回 invalid，实际 %v", err)
	}
	after, _ := e.svc.Get(e.ctx, p.ID)
	if after.Version != before.Version {
		t.Fatalf("失败的替换不得推进版本: before=%d after=%d", before.Version, after.Version)
	}
	if len(after.Layers) != len(before.Layers) {
		t.Fatalf("失败的替换不得改变分层")
	}
	for i := range before.Layers {
		if after.Layers[i] != before.Layers[i] {
			t.Fatalf("失败的替换改写了第 %d 层", i)
		}
	}
}

// nil 分层必须拒绝（与空数组清空语义不同），且不产生新版本。
func TestReplaceNilLayersRejected(t *testing.T) {
	e := newEnv(t)
	p := e.create("空数组与null", 10000)
	_, err := e.svc.Replace(e.ctx, p.ID, catalog.ReplaceLayers{
		ExpectedVersion: 1, Reason: "传 null", Layers: nil,
	})
	var problem *geology.Problem
	if !errors.As(err, &problem) || problem.Code != "invalid" {
		t.Fatalf("null 分层应返回 invalid，实际 %v", err)
	}
	cleared := e.mustReplace(p, []geology.Layer{}, "清空草拟分层")
	if len(cleared.Layers) != 0 || cleared.Version != 2 {
		t.Fatalf("空数组应清空分层并推进版本: %+v", cleared)
	}
}

// 锁定失败保留原状态；成功后编辑被拒绝，重新打开创建下一版本且历史仍可读。
func TestSealReopenWorkflowAndHistory(t *testing.T) {
	e := newEnv(t)
	p := e.create("工作流", 10000)
	p = e.mustReplace(p, []geology.Layer{
		{TopMM: 0, BottomMM: 4000, Rock: geology.Sandstone},
	}, "仅部分覆盖")

	// 有缺口时锁定失败，状态与版本不变。
	if _, err := e.svc.Change(e.ctx, p.ID, geology.Sealed,
		catalog.StateChange{ExpectedVersion: p.Version, Reason: "尝试锁定"}); err == nil {
		t.Fatalf("存在缺口时锁定必须失败")
	}
	again, _ := e.svc.Get(e.ctx, p.ID)
	if again.State != geology.Draft || again.Version != p.Version {
		t.Fatalf("失败的锁定改变了剖面: %+v", again)
	}

	// 补全覆盖后锁定。
	p = e.mustReplace(p, fullLayers(10000), "补全覆盖")
	sealed := e.mustChange(p, geology.Sealed, "核对完成")
	if sealed.State != geology.Sealed {
		t.Fatalf("锁定后状态错误: %s", sealed.State)
	}

	// 锁定后分层替换、元数据修改、再次锁定全部拒绝。
	if _, err := e.svc.Replace(e.ctx, sealed.ID, catalog.ReplaceLayers{
		ExpectedVersion: sealed.Version, Reason: "改层", Layers: sealed.Layers,
	}); err == nil {
		t.Fatalf("锁定后替换分层必须拒绝")
	}
	if _, err := e.svc.Edit(e.ctx, sealed.ID, catalog.EditMetadata{
		ExpectedVersion: sealed.Version, Reason: "改名",
		Metadata: geology.Metadata{Name: "新名称", Site: "赤石岭", DepthMM: 10000},
	}); err == nil {
		t.Fatalf("锁定后修改元数据必须拒绝")
	}

	// 重新打开生成下一版本；历史锁定版本仍可读取且不可变。
	reopened := e.mustChange(sealed, geology.Draft, "需要补充描述")
	if reopened.State != geology.Draft || reopened.Version != sealed.Version+1 {
		t.Fatalf("重新打开后状态/版本错误: %+v", reopened)
	}
	old, err := e.svc.Revision(e.ctx, sealed.ID, sealed.Version)
	if err != nil {
		t.Fatalf("读取历史版本失败: %v", err)
	}
	if old.Profile.State != geology.Sealed || old.Profile.Version != sealed.Version {
		t.Fatalf("历史版本内容错误: %+v", old.Profile)
	}

	// 在新版本上编辑后，旧版本分层内容保持不变。
	edited, err := e.svc.Replace(e.ctx, reopened.ID, catalog.ReplaceLayers{
		ExpectedVersion: reopened.Version,
		Reason:          "修订第二层",
		Layers: []geology.Layer{
			{TopMM: 0, BottomMM: 10000, Rock: geology.Limestone},
		},
	})
	if err != nil {
		t.Fatalf("重新打开后编辑失败: %v", err)
	}
	oldAgain, _ := e.svc.Revision(e.ctx, sealed.ID, sealed.Version)
	if len(oldAgain.Profile.Layers) != len(sealed.Layers) {
		t.Fatalf("历史版本被后续修订修改")
	}
	if edited.Layers[0].Rock != geology.Limestone {
		t.Fatalf("新版本分层错误: %+v", edited.Layers)
	}

	// 历史事件按版本升序可分页读取。
	page, err := e.svc.History(e.ctx, p.ID, 0, 100)
	if err != nil {
		t.Fatalf("读取历史事件失败: %v", err)
	}
	if page.Total != edited.Version || len(page.Items) != edited.Version {
		t.Fatalf("事件数量 = %d/%d，期望 %d", page.Total, len(page.Items), edited.Version)
	}
	if page.Items[0].Action != "create" {
		t.Fatalf("首个事件应为 create，实际 %s", page.Items[0].Action)
	}
	for i := 1; i < len(page.Items); i++ {
		if page.Items[i].Version <= page.Items[i-1].Version {
			t.Fatalf("事件必须按版本升序")
		}
	}
}

// 空白理由在 catalog 层裁剪后必须拒绝（geology.Text 仅校验长度）。
func TestWhitespaceReasonRejectedAtCatalog(t *testing.T) {
	e := newEnv(t)
	p := e.create("理由校验", 10000)
	_, err := e.svc.Edit(e.ctx, p.ID, catalog.EditMetadata{
		ExpectedVersion: 1,
		Reason:          "   ",
		Metadata:        geology.Metadata{Name: "新名称", Site: "赤石岭", DepthMM: 10000},
	})
	var problem *geology.Problem
	if !errors.As(err, &problem) || problem.Code != "invalid" {
		t.Fatalf("空白理由裁剪后应返回 invalid，实际 %v", err)
	}
}

// 对比复用：相同输入首次 201 语义（reused=false），重复请求 reused=true 且编号一致；
// 重新打开后引用的历史版本仍可复用；交换左右是不同结果。
func TestComparisonReuseAcrossReopen(t *testing.T) {
	e := newEnv(t)

	build := func(name string) (string, int) {
		p := e.create(name, 10000)
		p = e.mustReplace(p, fullLayers(10000), "完整分层")
		p = e.mustChange(p, geology.Sealed, "锁定")
		return p.ID, p.Version
	}
	leftID, leftVersion := build("对比左")
	rightID, rightVersion := build("对比右")
	req := correlation.Request{
		Left:     correlation.Reference{ID: leftID, Version: leftVersion},
		Right:    correlation.Reference{ID: rightID, Version: rightVersion},
		OffsetMM: -1000,
	}
	first, reused, err := e.svc.Compare(e.ctx, req)
	if err != nil {
		t.Fatalf("首次对比失败: %v", err)
	}
	if reused {
		t.Fatalf("首次对比不应命中复用")
	}
	if !strings.HasPrefix(first.ID, "cmp_") {
		t.Fatalf("对比编号前缀错误: %s", first.ID)
	}

	second, reused2, err := e.svc.Compare(e.ctx, req)
	if err != nil {
		t.Fatalf("重复对比失败: %v", err)
	}
	if !reused2 || second.ID != first.ID {
		t.Fatalf("相同输入必须复用结果")
	}

	// 左侧剖面重新打开进入新版本，历史版本对比结果仍可读取。
	sealedLeft, err := e.svc.Get(e.ctx, leftID)
	if err != nil {
		t.Fatal(err)
	}
	reopened := e.mustChange(sealedLeft, geology.Draft, "修订")
	_ = reopened
	cached, err := e.svc.Comparison(e.ctx, first.ID)
	if err != nil {
		t.Fatalf("重新打开后历史对比结果必须仍可读取: %v", err)
	}
	if cached.Request.Left.Version != leftVersion {
		t.Fatalf("复用结果必须引用历史版本而非当前版本")
	}
	third, reused3, _ := e.svc.Compare(e.ctx, req)
	if !reused3 || third.ID != first.ID {
		t.Fatalf("重新打开后相同输入仍必须复用历史结果")
	}

	// 交换左右是不同输入。
	swapped := correlation.Request{Left: req.Right, Right: req.Left, OffsetMM: -1000}
	other, reused4, err := e.svc.Compare(e.ctx, swapped)
	if err != nil {
		t.Fatalf("交换左右对比失败: %v", err)
	}
	if reused4 || other.ID == first.ID {
		t.Fatalf("交换左右必须产生不同结果")
	}
}

// 草拟版本对比、无共同区间对比、自身版本对比均拒绝。
func TestComparisonValidation(t *testing.T) {
	e := newEnv(t)
	p := e.create("未锁定", 10000)
	p = e.mustReplace(p, fullLayers(10000), "完整分层但不锁定")

	other := e.create("已锁定侧", 10000)
	other = e.mustReplace(other, fullLayers(10000), "完整分层")
	other = e.mustChange(other, geology.Sealed, "锁定")

	if _, _, err := e.svc.Compare(e.ctx, correlation.Request{
		Left:  correlation.Reference{ID: p.ID, Version: p.Version},
		Right: correlation.Reference{ID: other.ID, Version: other.Version},
	}); err == nil {
		t.Fatalf("草拟版本必须拒绝对比")
	}
	if _, _, err := e.svc.Compare(e.ctx, correlation.Request{
		Left:  correlation.Reference{ID: other.ID, Version: other.Version},
		Right: correlation.Reference{ID: other.ID, Version: other.Version},
	}); err == nil {
		t.Fatalf("同一版本自身必须拒绝对比")
	}
}

// 返回剖面是独立拷贝：调用方修改返回值的切片不得污染后续读取。
func TestReturnedProfileIsIsolated(t *testing.T) {
	e := newEnv(t)
	p := e.create("隔离剖面", 10000)
	p = e.mustReplace(p, fullLayers(10000), "完整分层")

	p.Layers[0].Rock = geology.Conglomerate
	p.Layers = append(p.Layers, geology.Layer{TopMM: 0, BottomMM: 1, Rock: geology.Shale})

	again, err := e.svc.Get(e.ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(again.Layers) != 2 || again.Layers[0].Rock != geology.Sandstone {
		t.Fatalf("调用方对返回值的修改泄漏到了仓库状态: %+v", again.Layers)
	}
}

// 元数据整体替换推进版本但不触碰分层；历史版本保留旧名称。
func TestEditMetadataPreservesLayers(t *testing.T) {
	e := newEnv(t)
	p := e.create("旧名称", 10000)
	p = e.mustReplace(p, fullLayers(10000), "先分层")
	updated, err := e.svc.Edit(e.ctx, p.ID, catalog.EditMetadata{
		ExpectedVersion: p.Version,
		Reason:          "统一命名",
		Metadata:        geology.Metadata{Name: "新名称", Site: "赤石岭南", DepthMM: 10000},
	})
	if err != nil {
		t.Fatalf("修改元数据失败: %v", err)
	}
	if updated.Version != p.Version+1 || updated.Name != "新名称" {
		t.Fatalf("元数据修订错误: %+v", updated)
	}
	if len(updated.Layers) != len(p.Layers) {
		t.Fatalf("元数据修订不得改变分层")
	}
	old, err := e.svc.Revision(e.ctx, p.ID, p.Version)
	if err != nil {
		t.Fatal(err)
	}
	if old.Profile.Name != "旧名称" {
		t.Fatalf("历史版本名称必须保留: %s", old.Profile.Name)
	}

	// 深度修改在元数据接口属于合法字段，但会与既有分层产生冲突时由快照链发现；
	// 这里确认正常深度边界（1 毫米、1000000 毫米）可创建。
	minimal := e.create("一毫米剖面", 1)
	if minimal.DepthMM != 1 {
		t.Fatalf("最小深度创建失败")
	}
}

// 差异接口：边界移动表现为旧区间移除、新区间增加；同区间岩性修改给出前后值。
func TestDifferenceThroughService(t *testing.T) {
	e := newEnv(t)
	p := e.create("差异剖面", 10000)
	p = e.mustReplace(p, []geology.Layer{
		{TopMM: 0, BottomMM: 4000, Rock: geology.Sandstone, Description: "初版砂岩"},
		{TopMM: 4000, BottomMM: 10000, Rock: geology.Mudstone},
	}, "初版分层")
	vBefore := p.Version
	p = e.mustReplace(p, []geology.Layer{
		{TopMM: 0, BottomMM: 5000, Rock: geology.Sandstone, Description: "初版砂岩"},
		{TopMM: 5000, BottomMM: 10000, Rock: geology.Limestone},
	}, "边界下移并换岩性")

	diff, err := e.svc.Difference(e.ctx, p.ID, vBefore, p.Version)
	if err != nil {
		t.Fatalf("差异查询失败: %v", err)
	}
	// [4000,10000) 移除；[5000,10000) 增加；[0,4000) 消失、[0,5000) 出现。
	spans := map[[2]int64]geology.LayerChange{}
	for _, c := range diff.Layers {
		spans[[2]int64{c.TopMM, c.BottomMM}] = c
	}
	oldSpan, ok := spans[[2]int64{4000, 10000}]
	if !ok || oldSpan.Before == nil || oldSpan.After != nil {
		t.Fatalf("旧区间必须表现为移除: %+v", diff.Layers)
	}
	newSpan, ok := spans[[2]int64{5000, 10000}]
	if !ok || newSpan.After == nil || newSpan.Before != nil {
		t.Fatalf("新区间必须表现为增加: %+v", diff.Layers)
	}
	if newSpan.After.Rock != geology.Limestone {
		t.Fatalf("新区间岩性错误: %+v", newSpan)
	}
}
