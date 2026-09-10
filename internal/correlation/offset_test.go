package correlation_test

import (
	"strings"
	"testing"

	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/correlation"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/geology"
)

// 奇数个共同标志层：中位数即中间值；残差与平均绝对残差可核对。
func TestSuggestMedianOdd(t *testing.T) {
	left := sealed(leftID, 10000, []geology.Layer{
		L(0, 600, geology.Sandstone),
		ML(600, 2500, geology.Mudstone, "M1"),
		ML(2500, 5500, geology.Shale, "M2"),
		ML(5500, 10000, geology.Limestone, "M3"),
	})
	right := sealed(rightID, 10000, []geology.Layer{
		L(0, 500, geology.Sandstone),
		ML(500, 2300, geology.Mudstone, "M1"),
		ML(2300, 5200, geology.Shale, "M2"),
		ML(5200, 10000, geology.Limestone, "M3"),
	})
	// 所需偏移（左顶 - 右顶）：100, 200, 300。
	proposal, err := correlation.Suggest(left, right,
		correlation.OffsetRequest{Left: ref(left), Right: ref(right)})
	if err != nil {
		t.Fatalf("偏移建议失败: %v", err)
	}
	if proposal.Comparison.OffsetMM != 200 {
		t.Fatalf("中位数偏移 = %d，期望 200", proposal.Comparison.OffsetMM)
	}
	if !proposal.Ambiguous {
		t.Fatalf("证据存在分歧时 Ambiguous 必须为真")
	}
	if proposal.MaxResidualMM != 100 {
		t.Fatalf("最大残差 = %d，期望 100", proposal.MaxResidualMM)
	}
	if mean := proposal.MeanAbsoluteResidualMM; mean < 66.66 || mean > 66.67 {
		t.Fatalf("平均绝对残差 = %v，期望 200/3 ≈ 66.667", mean)
	}
	if proposal.ExpectedOverlapMM != 9800 {
		t.Fatalf("预期共同区间 = %d，期望 9800", proposal.ExpectedOverlapMM)
	}
	gotOffsets := map[string]int64{}
	for _, e := range proposal.Evidence {
		gotOffsets[e.Marker] = e.RequiredOffsetMM
	}
	if gotOffsets["M1"] != 100 || gotOffsets["M2"] != 200 || gotOffsets["M3"] != 300 {
		t.Fatalf("证据偏移错误: %+v", proposal.Evidence)
	}
	// 建议对象必须可以直接提交为对比请求。
	if proposal.Comparison.Left != ref(left) || proposal.Comparison.Right != ref(right) {
		t.Fatalf("建议中的对比引用错误: %+v", proposal.Comparison)
	}
}

// 偶数个标志层：中间两项平均并向零取整（(-3+0)/2 = -1，而非 -2）。
// 标志层按忽略大小写的名称匹配，证据保留左侧名称。
func TestSuggestMedianEvenRoundsTowardZero(t *testing.T) {
	left := sealed(leftID, 10000, []geology.Layer{
		L(0, 1000, geology.Sandstone),
		ML(1000, 2000, geology.Mudstone, "M-1"),
		ML(2000, 3000, geology.Shale, "M-2"),
		ML(3000, 4000, geology.Limestone, "M-3"),
		ML(4000, 10000, geology.Conglomerate, "M-4"),
	})
	right := sealed(rightID, 10000, []geology.Layer{
		L(0, 1005, geology.Sandstone),
		ML(1005, 2003, geology.Mudstone, "m-1"),
		ML(2003, 3000, geology.Shale, "m-2"),
		ML(3000, 3998, geology.Limestone, "m-3"),
		ML(3998, 10000, geology.Conglomerate, "m-4"),
	})
	// 所需偏移：-5, -3, 0, 2；排序后中间两项 -3 与 0，向零取整得 -1。
	proposal, err := correlation.Suggest(left, right,
		correlation.OffsetRequest{Left: ref(left), Right: ref(right)})
	if err != nil {
		t.Fatalf("偏移建议失败: %v", err)
	}
	if proposal.Comparison.OffsetMM != -1 {
		t.Fatalf("偶数中位数向零取整 = %d，期望 -1", proposal.Comparison.OffsetMM)
	}
	found := false
	for _, e := range proposal.Evidence {
		if strings.EqualFold(e.Marker, "m-2") {
			found = true
			if e.Marker != "M-2" {
				t.Fatalf("证据应保留左侧标志层名称写法，实际 %q", e.Marker)
			}
			if e.RequiredOffsetMM != -3 || e.ResidualMM != 2 {
				t.Fatalf("M-2 证据错误: %+v", e)
			}
		}
	}
	if !found {
		t.Fatalf("忽略大小写的标志层匹配失败: %+v", proposal.Evidence)
	}
}

// 所有标志层一致时不存在分歧，残差全为零。
func TestSuggestUnambiguous(t *testing.T) {
	left := sealed(leftID, 10000, []geology.Layer{
		L(0, 3000, geology.Sandstone),
		ML(3000, 6000, geology.Mudstone, "A"),
		ML(6000, 10000, geology.Shale, "B"),
	})
	right := sealed(rightID, 10000, []geology.Layer{
		L(0, 1000, geology.Sandstone),
		ML(1000, 4000, geology.Mudstone, "A"),
		ML(4000, 10000, geology.Shale, "B"),
	})
	// 两个标志层所需偏移均为 2000。
	proposal, err := correlation.Suggest(left, right,
		correlation.OffsetRequest{Left: ref(left), Right: ref(right)})
	if err != nil {
		t.Fatalf("偏移建议失败: %v", err)
	}
	if proposal.Comparison.OffsetMM != 2000 || proposal.Ambiguous {
		t.Fatalf("一致证据应无分歧且偏移 2000: %+v", proposal)
	}
	if proposal.MaxResidualMM != 0 || proposal.MeanAbsoluteResidualMM != 0 {
		t.Fatalf("残差应为零: max=%d mean=%v", proposal.MaxResidualMM, proposal.MeanAbsoluteResidualMM)
	}
}

func TestSuggestRejections(t *testing.T) {
	// 没有共同标志层。
	left := sealed(leftID, 10000, []geology.Layer{
		ML(0, 4000, geology.Sandstone, "仅左侧有"),
		L(4000, 10000, geology.Mudstone),
	})
	right := sealed(rightID, 10000, []geology.Layer{
		ML(0, 4000, geology.Sandstone, "仅右侧有"),
		L(4000, 10000, geology.Mudstone),
	})
	if _, err := correlation.Suggest(left, right,
		correlation.OffsetRequest{Left: ref(left), Right: ref(right)}); err == nil {
		t.Fatalf("无共同标志层必须拒绝")
	}

	// 草拟版本不能给出建议。
	draft := left
	draft.State = geology.Draft
	if _, err := correlation.Suggest(draft, right,
		correlation.OffsetRequest{Left: ref(draft), Right: ref(right)}); err == nil {
		t.Fatalf("草拟版本的偏移建议必须拒绝")
	}

	// 同一版本自身。
	self := correlation.OffsetRequest{Left: ref(left), Right: ref(left)}
	if _, err := correlation.Suggest(left, left, self); err == nil {
		t.Fatalf("同一版本自身必须拒绝")
	}
}
