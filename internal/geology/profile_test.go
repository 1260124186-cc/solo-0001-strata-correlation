package geology

import "testing"

func TestMetadataDepthAndTextBounds(t *testing.T) {
	cases := []struct {
		name string
		meta Metadata
		ok   bool
	}{
		{"最小合法深度", Metadata{Name: "x", Site: "y", DepthMM: 1}, true},
		{"最大合法深度", Metadata{Name: "x", Site: "y", DepthMM: MaxDepth}, true},
		{"零深度", Metadata{Name: "x", Site: "y", DepthMM: 0}, false},
		{"负深度", Metadata{Name: "x", Site: "y", DepthMM: -1}, false},
		{"超过一千米", Metadata{Name: "x", Site: "y", DepthMM: MaxDepth + 1}, false},
		{"空白名称", Metadata{Name: "  ", Site: "y", DepthMM: 10}, false},
		{"缺地点", Metadata{Name: "x", Site: "", DepthMM: 10}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NormalizeMetadata(tc.meta)
			if tc.ok && err != nil {
				t.Fatalf("期望合法，实际被拒绝: %v", err)
			}
			if !tc.ok && err == nil {
				t.Fatalf("期望拒绝，实际通过: %+v", tc.meta)
			}
		})
	}
}

func TestMetadataTrimsWhitespace(t *testing.T) {
	got, err := NormalizeMetadata(Metadata{Name: "  赤石  ", Site: " 北坡 ", Note: " 备注 ", DepthMM: 10})
	if err != nil {
		t.Fatalf("合法元数据不应报错: %v", err)
	}
	if got.Name != "赤石" || got.Site != "北坡" || got.Note != "备注" {
		t.Fatalf("元数据裁剪错误: %+v", got)
	}
}

func runes(n int) string {
	b := make([]rune, n)
	for i := range b {
		b[i] = '字'
	}
	return string(b)
}

func TestTextLengthsAreRuneCount(t *testing.T) {
	// 名称上限 120 个字符（按 rune 计，而非字节）。
	if _, err := NormalizeMetadata(Metadata{Name: runes(120), Site: "地点", DepthMM: 1}); err != nil {
		t.Fatalf("120 字符名称应合法: %v", err)
	}
	if _, err := NormalizeMetadata(Metadata{Name: runes(121), Site: "地点", DepthMM: 1}); err == nil {
		t.Fatalf("121 字符名称必须拒绝")
	}
	// 单层描述 1000 字、标志层 80 字。
	layers := []Layer{{TopMM: 0, BottomMM: 1, Rock: Sandstone, Description: runes(1000)}}
	if err := ValidateLayers(layers, 10); err != nil {
		t.Fatalf("1000 字描述应合法: %v", err)
	}
	layers[0].Description = runes(1001)
	if err := ValidateLayers(layers, 10); err == nil {
		t.Fatalf("1001 字描述必须拒绝")
	}
	layers[0].Description = ""
	layers[0].Marker = runes(80)
	if err := ValidateLayers(layers, 10); err != nil {
		t.Fatalf("80 字标志层应合法: %v", err)
	}
	layers[0].Marker = runes(81)
	if err := ValidateLayers(layers, 10); err == nil {
		t.Fatalf("81 字标志层必须拒绝")
	}
}
