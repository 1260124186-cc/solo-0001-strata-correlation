package geology

import (
	"errors"
	"testing"
	"time"
)

var fixedNow = time.Date(2026, 9, 10, 9, 0, 0, 0, time.UTC)

func fullDraft() Profile {
	p := draftProfile(10000,
		layer(0, 4000, Sandstone),
		layer(4000, 10000, Mudstone),
	)
	return p
}

// 锁定前必须完整覆盖 [0, depth)：有缺口时锁定失败且输入剖面不被修改。
func TestChangeStateSealRejectsGaps(t *testing.T) {
	p := draftProfile(10000, layer(0, 4000, Sandstone))
	before := p.Clone()
	_, err := ChangeState(p, Sealed, p.Version, "锁定", fixedNow)
	if err == nil {
		t.Fatalf("存在缺口时必须拒绝锁定")
	}
	if problemCode(t, err) != "conflict" {
		t.Fatalf("缺口锁定应返回 conflict，实际 %v", err)
	}
	if p.State != before.State || p.Version != before.Version || len(p.Layers) != len(before.Layers) {
		t.Fatalf("失败的锁定不得改变剖面")
	}
}

// 锁定成功：版本递增、状态切换、生成 seal 事件。
func TestChangeStateSealSuccess(t *testing.T) {
	p := fullDraft()
	rev, err := ChangeState(p, Sealed, 1, "分层已核对", fixedNow)
	if err != nil {
		t.Fatalf("完整覆盖时应可锁定: %v", err)
	}
	if rev.Profile.State != Sealed || rev.Profile.Version != 2 {
		t.Fatalf("锁定后状态/版本错误: state=%s version=%d", rev.Profile.State, rev.Profile.Version)
	}
	if rev.Event.Action != "seal" || rev.Event.Version != 2 || !rev.Event.At.Equal(fixedNow) {
		t.Fatalf("seal 事件错误: %+v", rev.Event)
	}
}

// 已锁定剖面不可编辑；重新打开产生下一版本且保留原有分层。
func TestSealedImmutableAndReopen(t *testing.T) {
	sealed, err := ChangeState(fullDraft(), Sealed, 1, "锁定", fixedNow)
	if err != nil {
		t.Fatalf("锁定失败: %v", err)
	}
	if err = CheckEditable(sealed.Profile, 2); err == nil {
		t.Fatalf("锁定剖面必须不可编辑")
	} else if problemCode(t, err) != "conflict" {
		t.Fatalf("锁定编辑应返回 conflict，实际 %v", err)
	}
	// 错误的预期版本必须先于状态检查暴露为冲突。
	if err = CheckEditable(sealed.Profile, 1); err == nil {
		t.Fatalf("旧预期版本必须被拒绝")
	}

	reopened, err := ChangeState(sealed.Profile, Draft, 2, "需要修订", fixedNow.Add(time.Minute))
	if err != nil {
		t.Fatalf("重新打开失败: %v", err)
	}
	if reopened.Profile.State != Draft || reopened.Profile.Version != 3 {
		t.Fatalf("重新打开后状态/版本错误: %+v", reopened.Profile)
	}
	if reopened.Event.Action != "reopen" || reopened.Event.Version != 3 {
		t.Fatalf("reopen 事件错误: %+v", reopened.Event)
	}
	if len(reopened.Profile.Layers) != 2 {
		t.Fatalf("重新打开不得改变分层数量")
	}
	// 历史对象保持已锁定状态，不能被后续修订覆盖。
	if sealed.Profile.State != Sealed || sealed.Profile.Version != 2 {
		t.Fatalf("历史版本被重新打开所修改")
	}
}

// 重复进入同一状态、空白理由均必须拒绝。
func TestChangeStateGuards(t *testing.T) {
	p := fullDraft()
	if _, err := ChangeState(p, Draft, 1, "重复打开", fixedNow); err == nil {
		t.Fatalf("草拟状态重新打开必须拒绝")
	}
	if _, err := ChangeState(p, Sealed, 1, "", fixedNow); err == nil {
		t.Fatalf("空理由必须拒绝")
	}
	if _, err := ChangeState(p, Sealed, 99, "版本错误", fixedNow); err == nil {
		t.Fatalf("错误的预期版本必须拒绝")
	}
	// 缺口导致的冲突必须保留领域 Problem 类型，供上层映射为 409。
	gapped := draftProfile(10000, layer(0, 4000, Sandstone))
	_, err := ChangeState(gapped, Sealed, 1, "锁定", fixedNow)
	var pErr *Problem
	if !errors.As(err, &pErr) {
		t.Fatalf("领域错误应保留 Problem 类型，实际 %T: %v", err, err)
	}
}

// 历史版本之间不可变：DifferenceOf 只读取两个快照，不修改它们。
func TestHistorySnapshotsRemainReadable(t *testing.T) {
	base := fullDraft()
	sealed, _ := ChangeState(base, Sealed, 1, "锁定", fixedNow)
	reopened, _ := ChangeState(sealed.Profile, Draft, 2, "修订", fixedNow.Add(time.Minute))

	edited := reopened.Profile.Clone()
	edited.Layers[0].Rock = Limestone
	edited.Layers[0].BottomMM = 5000
	edited.Layers[1].TopMM = 5000
	edited.Version = 4
	edited.UpdatedAt = fixedNow.Add(2 * time.Minute)

	diff, err := DifferenceOf(sealed.Profile, edited)
	if err != nil {
		t.Fatalf("差异计算失败: %v", err)
	}
	if diff.FromVersion != 2 || diff.ToVersion != 4 {
		t.Fatalf("差异版本引用错误: %+v", diff)
	}
	if len(diff.Layers) == 0 {
		t.Fatalf("分层变化必须体现在差异中")
	}
	// 重新计算差异不得改变任何输入快照。
	if sealed.Profile.State != Sealed || reopened.Profile.Layers[0].Rock != Sandstone {
		t.Fatalf("差异计算修改了历史快照")
	}
}
