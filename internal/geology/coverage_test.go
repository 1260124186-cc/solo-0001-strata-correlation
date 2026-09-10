package geology

import (
	"testing"
	"time"
)

func draftProfile(depth int64, layers ...Layer) Profile {
	now := time.Date(2026, 9, 10, 8, 0, 0, 0, time.UTC)
	return Profile{
		ID:        "prf_00000000000000000000000000000001",
		Metadata:  Metadata{Name: "测试剖面", Site: "测试地点", DepthMM: depth},
		Layers:    layers,
		State:     Draft,
		Version:   1,
		CreatedAt: now,
		UpdatedAt: now,
	}
}

// 草拟状态允许空隙：起始缺口、层间缺口和末端缺口都必须暴露出来。
func TestCoverageReportsGaps(t *testing.T) {
	p := draftProfile(10000,
		layer(1000, 2000, Sandstone),
		layer(3000, 4000, Mudstone),
		layer(5000, 7000, Shale),
	)
	c := CoverageOf(p)
	want := []Interval{{0, 1000}, {2000, 3000}, {4000, 5000}, {7000, 10000}}
	if len(c.Gaps) != len(want) {
		t.Fatalf("缺口数量 = %d，期望 %d（%+v）", len(c.Gaps), len(want), c.Gaps)
	}
	for i := range want {
		if c.Gaps[i] != want[i] {
			t.Fatalf("缺口 %d = %+v，期望 %+v；全部 %+v", i, c.Gaps[i], want[i], c.Gaps)
		}
	}
	if c.CoveredMM != 4000 {
		t.Fatalf("已覆盖厚度 = %d，期望 4000", c.CoveredMM)
	}
	if c.Ready {
		t.Fatalf("存在缺口的草拟剖面不能标记为可锁定")
	}
}

// 完整覆盖 [0, depth) 时 Ready 为真，unknown 厚度单独统计但不影响覆盖。
func TestCoverageReadyWithUnknown(t *testing.T) {
	p := draftProfile(10000,
		layer(0, 4000, Sandstone),
		layer(4000, 7000, Unknown),
		layer(7000, 10000, Mudstone),
	)
	c := CoverageOf(p)
	if len(c.Gaps) != 0 {
		t.Fatalf("完整覆盖不应有缺口，实际 %+v", c.Gaps)
	}
	if !c.Ready {
		t.Fatalf("完整覆盖应可锁定")
	}
	if c.CoveredMM != 10000 || c.UnknownMM != 3000 {
		t.Fatalf("覆盖 %d / unknown %d，期望 10000 / 3000", c.CoveredMM, c.UnknownMM)
	}
	thickness := map[Lithology]int64{}
	for _, item := range c.Composition {
		thickness[item.Rock] = item.ThicknessMM
	}
	if thickness[Sandstone] != 4000 || thickness[Mudstone] != 3000 || thickness[Unknown] != 3000 {
		t.Fatalf("岩性厚度统计错误: %+v", c.Composition)
	}
}

// 左闭右开：边界点属于其下方分层；剖面底端不属于任何层。
func TestAtDepthHalfOpenBoundaries(t *testing.T) {
	p := draftProfile(10000,
		layer(0, 4000, Sandstone),
		layer(4000, 10000, Mudstone),
	)
	// 边界点 4000 属于下方泥岩层。
	pt, err := AtDepth(p, 4000)
	if err != nil {
		t.Fatalf("层内深度查询失败: %v", err)
	}
	if pt.Layer == nil || pt.Layer.Rock != Mudstone {
		t.Fatalf("深度 4000 应属于泥岩下层，实际 %+v", pt.Layer)
	}
	if pt.DistanceFromTopMM != 0 || pt.DistanceToBottomMM != 6000 {
		t.Fatalf("边界点距离计算错误: fromTop=%d toBottom=%d", pt.DistanceFromTopMM, pt.DistanceToBottomMM)
	}
	// 剖面底端（depth_mm）不包含。
	if _, err = AtDepth(p, 10000); err == nil {
		t.Fatalf("深度等于总深度必须被拒绝（右开）")
	}
	// 负深度同样非法。
	if _, err = AtDepth(p, -1); err == nil {
		t.Fatalf("负深度必须被拒绝")
	}
}

// 落在缺口里的深度应返回缺口区间，而不是相邻层。
func TestAtDepthInsideGap(t *testing.T) {
	p := draftProfile(10000,
		layer(0, 2000, Sandstone),
		layer(4000, 6000, Mudstone),
	)
	pt, err := AtDepth(p, 3000)
	if err != nil {
		t.Fatalf("缺口内深度不应报错: %v", err)
	}
	if pt.Layer != nil {
		t.Fatalf("缺口内深度不应命中分层，实际 %+v", pt.Layer)
	}
	want := Interval{2000, 4000}
	if pt.Gap == nil || *pt.Gap != want {
		t.Fatalf("缺口 = %+v，期望 %+v", pt.Gap, want)
	}
	if pt.DistanceFromTopMM != 1000 || pt.DistanceToBottomMM != 1000 {
		t.Fatalf("缺口距离错误: fromTop=%d toBottom=%d", pt.DistanceFromTopMM, pt.DistanceToBottomMM)
	}
}

// 起始缺口与末端缺口同样可查询。
func TestAtDepthLeadingAndTrailingGap(t *testing.T) {
	p := draftProfile(10000, layer(2000, 7000, Sandstone))
	if pt, err := AtDepth(p, 500); err != nil || pt.Gap == nil || *pt.Gap != (Interval{0, 2000}) {
		t.Fatalf("起始缺口查询错误: %+v err=%v", pt.Gap, err)
	}
	if pt, err := AtDepth(p, 9000); err != nil || pt.Gap == nil || *pt.Gap != (Interval{7000, 10000}) {
		t.Fatalf("末端缺口查询错误: %+v err=%v", pt.Gap, err)
	}
}

// 锁定状态的剖面若存在缺口，整体校验必须失败。
func TestValidateRejectsSealedWithGap(t *testing.T) {
	p := draftProfile(10000, layer(0, 4000, Sandstone))
	p.State = Sealed
	p.Version = 2
	if err := p.Validate(); err == nil {
		t.Fatalf("带缺口的锁定剖面必须校验失败")
	}
}
