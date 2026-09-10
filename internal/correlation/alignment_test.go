package correlation_test

import (
	"testing"
	"time"

	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/correlation"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/geology"
)

var testClock = time.Date(2026, 9, 10, 10, 0, 0, 0, time.UTC)

func sealed(id string, depth int64, layers []geology.Layer) geology.Profile {
	now := testClock
	return geology.Profile{
		ID:        id,
		Metadata:  geology.Metadata{Name: "剖面-" + id[len(id)-4:], Site: "测试地点", DepthMM: depth},
		Layers:    layers,
		State:     geology.Sealed,
		Version:   3,
		CreatedAt: now,
		UpdatedAt: now,
	}
}

func L(top, bottom int64, rock geology.Lithology) geology.Layer {
	return geology.Layer{TopMM: top, BottomMM: bottom, Rock: rock}
}

func ML(top, bottom int64, rock geology.Lithology, marker string) geology.Layer {
	l := L(top, bottom, rock)
	l.Marker = marker
	return l
}

func ref(p geology.Profile) correlation.Reference {
	return correlation.Reference{ID: p.ID, Version: p.Version}
}

const leftID = "prf_00000000000000000000000000000001"
const rightID = "prf_00000000000000000000000000000002"

// 右侧深度加偏移后，按双方边界切分共同区间并累计 equal/different。
func TestAlignSplitsCommonInterval(t *testing.T) {
	left := sealed(leftID, 10000, []geology.Layer{
		L(0, 4000, geology.Sandstone),
		L(4000, 10000, geology.Mudstone),
	})
	right := sealed(rightID, 10000, []geology.Layer{
		L(0, 4000, geology.Sandstone),
		L(4000, 10000, geology.Mudstone),
	})
	req := correlation.Request{Left: ref(left), Right: ref(right), OffsetMM: -2000}
	res, err := correlation.Align(left, right, req, testClock)
	if err != nil {
		t.Fatalf("对齐失败: %v", err)
	}
	want := []correlation.Segment{
		{0, 2000, geology.Sandstone, geology.Sandstone, "equal"},
		{2000, 4000, geology.Sandstone, geology.Mudstone, "different"},
		{4000, 8000, geology.Mudstone, geology.Mudstone, "equal"},
	}
	if len(res.Segments) != len(want) {
		t.Fatalf("区间数量 = %d，期望 %d：%+v", len(res.Segments), len(want), res.Segments)
	}
	for i := range want {
		if res.Segments[i] != want[i] {
			t.Fatalf("区间 %d = %+v，期望 %+v", i, res.Segments[i], want[i])
		}
	}
	if res.OverlapMM != 8000 || res.KnownMM != 8000 || res.EqualMM != 6000 {
		t.Fatalf("累计厚度错误: overlap=%d known=%d equal=%d", res.OverlapMM, res.KnownMM, res.EqualMM)
	}
	if res.Similarity == nil {
		t.Fatalf("已知区间存在时 similarity 不能为空")
	}
	if diff := *res.Similarity - 0.75; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("similarity = %v，期望 0.75", *res.Similarity)
	}
}

// 任一侧 unknown 的区间关系为 unknown，不计入 known_mm；仍有已知区间时给出比例。
func TestAlignUnknownExcludedFromKnown(t *testing.T) {
	left := sealed(leftID, 10000, []geology.Layer{
		L(0, 3000, geology.Sandstone),
		L(3000, 10000, geology.Mudstone),
	})
	right := sealed(rightID, 10000, []geology.Layer{
		L(0, 5000, geology.Unknown),
		L(5000, 10000, geology.Sandstone),
	})
	res, err := correlation.Align(left, right, correlation.Request{Left: ref(left), Right: ref(right)}, testClock)
	if err != nil {
		t.Fatalf("对齐失败: %v", err)
	}
	for _, s := range res.Segments {
		if (s.LeftRock == geology.Unknown || s.RightRock == geology.Unknown) && s.Relation != "unknown" {
			t.Fatalf("含 unknown 的区间关系必须是 unknown: %+v", s)
		}
	}
	if res.OverlapMM != 10000 || res.KnownMM != 5000 || res.EqualMM != 0 {
		t.Fatalf("厚度累计错误: overlap=%d known=%d equal=%d", res.OverlapMM, res.KnownMM, res.EqualMM)
	}
	if res.Similarity == nil || *res.Similarity != 0 {
		t.Fatalf("无相同岩性时 similarity 应为 0，实际 %v", res.Similarity)
	}
}

// 共同区间全部 unknown：similarity 必须为 null。
func TestAlignAllUnknownSimilarityNull(t *testing.T) {
	left := sealed(leftID, 10000, []geology.Layer{L(0, 10000, geology.Unknown)})
	right := sealed(rightID, 10000, []geology.Layer{L(0, 10000, geology.Unknown)})
	res, err := correlation.Align(left, right, correlation.Request{Left: ref(left), Right: ref(right)}, testClock)
	if err != nil {
		t.Fatalf("对齐失败: %v", err)
	}
	if res.OverlapMM != 10000 {
		t.Fatalf("重叠厚度 = %d，期望 10000", res.OverlapMM)
	}
	if res.KnownMM != 0 || res.Similarity != nil {
		t.Fatalf("全部 unknown 时 known 必须为 0 且 similarity 为 null，实际 known=%d sim=%v", res.KnownMM, res.Similarity)
	}
}

func TestAlignRejections(t *testing.T) {
	left := sealed(leftID, 10000, []geology.Layer{L(0, 10000, geology.Sandstone)})
	right := sealed(rightID, 10000, []geology.Layer{L(0, 10000, geology.Mudstone)})

	if _, err := correlation.Align(left, right, correlation.Request{Left: ref(left), Right: ref(right), OffsetMM: 20000}, testClock); err == nil {
		t.Fatalf("偏移后无共同区间必须拒绝")
	}

	// 同一版本自身对比。
	self := correlation.Request{Left: ref(left), Right: ref(left)}
	if _, err := correlation.Align(left, left, self, testClock); err == nil {
		t.Fatalf("同一版本自身对比必须拒绝")
	}

	// 非锁定输入。
	draft := left
	draft.State = geology.Draft
	if _, err := correlation.Align(draft, right, correlation.Request{Left: ref(draft), Right: ref(right)}, testClock); err == nil {
		t.Fatalf("草拟版本对比必须拒绝")
	}

	// 偏移超出一千米范围。
	if err := (correlation.Request{Left: ref(left), Right: ref(right), OffsetMM: geology.MaxDepth + 1}).Validate(); err == nil {
		t.Fatalf("超过 +1000000 的偏移必须拒绝")
	}
	if err := (correlation.Request{Left: ref(left), Right: ref(right), OffsetMM: -geology.MaxDepth - 1}).Validate(); err == nil {
		t.Fatalf("低于 -1000000 的偏移必须拒绝")
	}
	// 边界值合法。
	if err := (correlation.Request{Left: ref(left), Right: ref(right), OffsetMM: geology.MaxDepth}).Validate(); err != nil {
		t.Fatalf("偏移恰好 +1000000 应合法: %v", err)
	}
}

// 相同输入（算法版本固定）必须生成同一编号；交换左右或改偏移是不同输入。
func TestRequestKeyDeterminism(t *testing.T) {
	req := correlation.Request{Left: correlation.Reference{leftID, 3}, Right: correlation.Reference{rightID, 3}, OffsetMM: -2000}
	again := req
	if req.Key() != again.Key() {
		t.Fatalf("相同输入编号不稳定")
	}
	swapped := correlation.Request{Left: again.Right, Right: again.Left, OffsetMM: -2000}
	if swapped.Key() == req.Key() {
		t.Fatalf("交换左右必须产生不同编号")
	}
	shifted := req
	shifted.OffsetMM = 0
	if shifted.Key() == req.Key() {
		t.Fatalf("修改偏移必须产生不同编号")
	}
}

// 对齐结果中的标志层证据：右侧位置按偏移换算到共同坐标。
func TestAlignMarkersShiftedByOffset(t *testing.T) {
	left := sealed(leftID, 10000, []geology.Layer{
		L(0, 3000, geology.Sandstone),
		ML(3000, 10000, geology.Mudstone, "凝灰A"),
	})
	right := sealed(rightID, 10000, []geology.Layer{
		L(0, 5000, geology.Sandstone),
		ML(5000, 10000, geology.Mudstone, "凝灰A"),
	})
	res, err := correlation.Align(left, right, correlation.Request{Left: ref(left), Right: ref(right), OffsetMM: -2000}, testClock)
	if err != nil {
		t.Fatalf("对齐失败: %v", err)
	}
	if len(res.Markers) != 1 {
		t.Fatalf("共同标志层数量 = %d，期望 1", len(res.Markers))
	}
	m := res.Markers[0]
	if m.Name != "凝灰A" || m.LeftMM != 3000 || m.RightMM != 3000 || m.DifferenceMM != 0 {
		t.Fatalf("标志层换算错误: %+v", m)
	}
}

// 偏移符号方向：右侧顶界 5000 + 偏移(-1000) = 共同坐标 4000。
// 差异（rightMM-leftMM）必须等于所采用的偏移。
func TestAlignMarkerOffsetSign(t *testing.T) {
	left := sealed(leftID, 10000, []geology.Layer{
		L(0, 3000, geology.Sandstone),
		ML(3000, 10000, geology.Mudstone, "K"),
	})
	right := sealed(rightID, 10000, []geology.Layer{
		L(0, 5000, geology.Sandstone),
		ML(5000, 10000, geology.Mudstone, "K"),
	})
	res, err := correlation.Align(left, right, correlation.Request{Left: ref(left), Right: ref(right), OffsetMM: -1000}, testClock)
	if err != nil {
		t.Fatalf("对齐失败: %v", err)
	}
	if len(res.Markers) != 1 {
		t.Fatalf("标志层数量错误: %d", len(res.Markers))
	}
	m := res.Markers[0]
	if m.LeftMM != 3000 || m.RightMM != 4000 || m.DifferenceMM != 1000 {
		t.Fatalf("负偏移换算错误: %+v", m)
	}
}

// 对齐结果必须是值拷贝：调用方修改返回的切片不得污染再次对齐。
func TestAlignResultNotAliased(t *testing.T) {
	left := sealed(leftID, 10000, []geology.Layer{L(0, 10000, geology.Sandstone)})
	right := sealed(rightID, 10000, []geology.Layer{L(0, 10000, geology.Sandstone)})
	req := correlation.Request{Left: ref(left), Right: ref(right)}
	first, err := correlation.Align(left, right, req, testClock)
	if err != nil {
		t.Fatal(err)
	}
	first.Segments[0].Relation = "tampered"
	second, err := correlation.Align(left, right, req, testClock.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if second.Segments[0].Relation != "equal" {
		t.Fatalf("重复对齐结果被先前调用方的修改污染: %+v", second.Segments)
	}
}
