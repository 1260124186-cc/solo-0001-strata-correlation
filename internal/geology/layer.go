package geology

import (
	"fmt"
	"sort"
	"strings"
)

type Lithology string

const (
	Sandstone    Lithology = "sandstone"
	Mudstone     Lithology = "mudstone"
	Limestone    Lithology = "limestone"
	Shale        Lithology = "shale"
	Conglomerate Lithology = "conglomerate"
	Unknown      Lithology = "unknown"
)

type Rock struct {
	Code  Lithology `json:"code"`
	Label string    `json:"label"`
}

func Rocks() []Rock {
	return []Rock{{Sandstone, "砂岩"}, {Mudstone, "泥岩"}, {Limestone, "灰岩"}, {Shale, "页岩"}, {Conglomerate, "砾岩"}, {Unknown, "未知"}}
}

func ValidRock(r Lithology) bool {
	for _, rock := range Rocks() {
		if r == rock.Code {
			return true
		}
	}
	return false
}

type Layer struct {
	TopMM       int64     `json:"top_mm"`
	BottomMM    int64     `json:"bottom_mm"`
	Rock        Lithology `json:"rock"`
	Description string    `json:"description"`
	Marker      string    `json:"marker"`
}

func NormalizeLayers(in []Layer, depth int64) ([]Layer, error) {
	layers := append([]Layer{}, in...)
	for i := range layers {
		layers[i].Description = strings.TrimSpace(layers[i].Description)
		layers[i].Marker = strings.TrimSpace(layers[i].Marker)
	}
	sort.Slice(layers, func(i, j int) bool {
		if layers[i].TopMM != layers[j].TopMM {
			return layers[i].TopMM < layers[j].TopMM
		}
		return layers[i].BottomMM < layers[j].BottomMM
	})
	return layers, ValidateLayers(layers, depth)
}

// LayersEqual 判断两段“已规范化”的分层是否描述同一套岩层。
// 调用方必须先用 NormalizeLayers 处理两侧：文字已 trim、顺序已按
// (top_mm, bottom_mm) 排定，因此这里按位置逐字段比较即可，输入数组
// 顺序本身不构成业务差异。深度、岩性、描述或标志层名称的任何不同
// （包括标志层大小写）都视为业务变化。
func LayersEqual(a, b []Layer) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func ValidateLayers(layers []Layer, depth int64) error {
	if len(layers) > MaxLayers {
		return Invalid("layers", "最多允许 500 层")
	}
	markers := make(map[string]bool)
	for i, layer := range layers {
		field := fmt.Sprintf("layers[%d]", i)
		if layer.TopMM < 0 || layer.BottomMM <= layer.TopMM || layer.BottomMM > depth {
			return Invalid(field, "深度必须为正厚度且位于剖面范围内")
		}
		if i > 0 && layer.TopMM < layers[i-1].BottomMM {
			return Invalid(field, "分层不能重叠")
		}
		if !ValidRock(layer.Rock) {
			return Invalid(field+".rock", "不支持的岩性")
		}
		if err := Text(field+".description", layer.Description, 0, 1000); err != nil {
			return err
		}
		if err := Text(field+".marker", layer.Marker, 0, 80); err != nil {
			return err
		}
		if strings.TrimSpace(layer.Marker) != layer.Marker || strings.TrimSpace(layer.Description) != layer.Description {
			return Invalid(field, "分层文字未规范化")
		}
		if layer.Marker != "" {
			key := strings.ToLower(layer.Marker)
			if markers[key] {
				return Invalid(field+".marker", "同一剖面的标志层名称不能重复")
			}
			markers[key] = true
		}
	}
	return nil
}
