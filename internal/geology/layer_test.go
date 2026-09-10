package geology

import (
	"errors"
	"testing"
)

func layer(top, bottom int64, rock Lithology) Layer {
	return Layer{TopMM: top, BottomMM: bottom, Rock: rock}
}

func markerLayer(top, bottom int64, rock Lithology, marker string) Layer {
	l := layer(top, bottom, rock)
	l.Marker = marker
	return l
}

func problemCode(t *testing.T, err error) string {
	t.Helper()
	var p *Problem
	if !errors.As(err, &p) {
		t.Fatalf("期望 *geology.Problem，实际 %T: %v", err, err)
	}
	return p.Code
}

// 输入按顶部深度排序；首尾相接（左闭右开）的分层允许存在。
func TestNormalizeLayersSortsAndKeepsTouching(t *testing.T) {
	in := []Layer{
		layer(4000, 10000, Mudstone),
		layer(2000, 4000, Shale),
		layer(0, 2000, Sandstone),
	}
	got, err := NormalizeLayers(in, 10000)
	if err != nil {
		t.Fatalf("首尾相接的分层应通过校验: %v", err)
	}
	for i := 1; i < len(got); i++ {
		if got[i].TopMM < got[i-1].TopMM {
			t.Fatalf("分层未按顶部深度排序: %+v", got)
		}
		if got[i].TopMM != got[i-1].BottomMM {
			t.Fatalf("相邻分层应在 %d 处相接", got[i-1].BottomMM)
		}
	}
	// NormalizeLayers 不应改写调用方切片的顺序。
	if in[0].TopMM != 4000 {
		t.Fatalf("归一化过程修改了输入切片")
	}
}

// 描述与标志层名称会被 TrimSpace；规范化后仍有空白包裹则拒绝，
// 因此“先 trim 再校验”必须对同一批数据生效。
func TestNormalizeLayersTrimsText(t *testing.T) {
	in := []Layer{
		layer(0, 4000, Sandstone),
	}
	in[0].Description = "  细粒砂岩  "
	in[0].Marker = " 凝灰标志 "
	got, err := NormalizeLayers(in, 10000)
	if err != nil {
		t.Fatalf("首尾空白应被规范化: %v", err)
	}
	if got[0].Description != "细粒砂岩" || got[0].Marker != "凝灰标志" {
		t.Fatalf("文字未正确裁剪: %+v", got[0])
	}
}

func TestValidateLayersRejectsOverlap(t *testing.T) {
	cases := []struct {
		name   string
		layers []Layer
	}{
		{"部分重叠", []Layer{layer(0, 4000, Sandstone), layer(2000, 6000, Mudstone)}},
		{"同一区间重复", []Layer{layer(0, 4000, Sandstone), layer(0, 4000, Mudstone)}},
		{"乱序输入同样识别重叠", []Layer{layer(2000, 6000, Mudstone), layer(0, 4000, Sandstone)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// 直接走 NormalizeLayers：排序后仍必须能识别重叠。
			if _, err := NormalizeLayers(tc.layers, 10000); err == nil {
				t.Fatalf("重叠分层必须被拒绝")
			} else if problemCode(t, err) != "invalid" {
				t.Fatalf("重叠应返回 invalid，实际 %v", err)
			}
		})
	}
}

func TestValidateLayersDepthBoundaries(t *testing.T) {
	depth := int64(10000)
	cases := []struct {
		name   string
		layers []Layer
	}{
		{"负顶部深度", []Layer{layer(-1, 4000, Sandstone)}},
		{"零厚度", []Layer{layer(4000, 4000, Sandstone)}},
		{"负厚度", []Layer{layer(4000, 3000, Sandstone)}},
		{"底界超过总深度", []Layer{layer(0, depth+1, Sandstone)}},
		{"顶界等于总深度无法容纳正厚度", []Layer{layer(depth, depth+1, Sandstone)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateLayers(tc.layers, depth)
			if err == nil {
				t.Fatalf("越界/非正厚度分层必须被拒绝: %+v", tc.layers)
			}
			if problemCode(t, err) != "invalid" {
				t.Fatalf("期望 invalid，实际 %v", err)
			}
		})
	}
}

// 边界值本身合法：零顶界、底界恰好等于总深度。
func TestValidateLayersAcceptsExactBounds(t *testing.T) {
	layers := []Layer{layer(0, 10000, Sandstone)}
	if err := ValidateLayers(layers, 10000); err != nil {
		t.Fatalf("覆盖 [0, depth) 的单层应合法: %v", err)
	}
}

func TestValidateLayersRockCodes(t *testing.T) {
	if err := ValidateLayers([]Layer{layer(0, 10, Unknown)}, 10000); err != nil {
		t.Fatalf("unknown 是受支持的岩性: %v", err)
	}
	err := ValidateLayers([]Layer{layer(0, 10, Lithology("granite"))}, 10000)
	if err == nil {
		t.Fatalf("未知岩性代码必须被拒绝")
	}
	if problemCode(t, err) != "invalid" {
		t.Fatalf("期望 invalid，实际 %v", err)
	}
}

func TestValidateLayersMarkerUniqueCaseInsensitive(t *testing.T) {
	layers := []Layer{
		markerLayer(0, 2000, Sandstone, "Tuff-A"),
		markerLayer(2000, 4000, Mudstone, "tuff-a"),
	}
	err := ValidateLayers(layers, 10000)
	if err == nil {
		t.Fatalf("忽略大小写后重复的标志层必须被拒绝")
	}
	if problemCode(t, err) != "invalid" {
		t.Fatalf("期望 invalid，实际 %v", err)
	}
}

func TestValidateLayersCapacity(t *testing.T) {
	makeLayers := func(n int) []Layer {
		layers := make([]Layer, n)
		for i := range layers {
			top := int64(i)
			layers[i] = layer(top, top+1, Sandstone)
		}
		return layers
	}
	if err := ValidateLayers(makeLayers(MaxLayers), 10000); err != nil {
		t.Fatalf("恰好 500 层应允许: %v", err)
	}
	err := ValidateLayers(makeLayers(MaxLayers+1), 10000)
	if err == nil {
		t.Fatalf("第 501 层必须被拒绝")
	}
	if problemCode(t, err) != "invalid" {
		t.Fatalf("期望 invalid，实际 %v", err)
	}
}
