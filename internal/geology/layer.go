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
	return NormalizeLayersLegacy(in, depth, nil)
}

// NormalizeLayersLegacy is the historical-data path: collisions already
// present in a grandfathered revision keep being accepted when the profile
// is edited, but any newly introduced equivalent spelling is rejected.
func NormalizeLayersLegacy(in []Layer, depth int64, legacy LegacyMarkers) ([]Layer, error) {
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
	return layers, validateLayers(layers, depth, legacy)
}

func ValidateLayers(layers []Layer, depth int64) error {
	return validateLayers(layers, depth, nil)
}

func validateLayers(layers []Layer, depth int64, legacy LegacyMarkers) error {
	if len(layers) > MaxLayers {
		return Invalid("layers", "最多允许 500 层")
	}
	seen := make(map[string][]string)
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
		if err := Text(field+".marker", layer.Marker, 0, MarkerNameMax); err != nil {
			return err
		}
		if strings.TrimSpace(layer.Marker) != layer.Marker || strings.TrimSpace(layer.Description) != layer.Description {
			return Invalid(field, "分层文字未规范化")
		}
		if layer.Marker == "" {
			continue
		}
		key := MarkerKey(layer.Marker)
		prior := seen[key]
		if len(prior) > 0 && !legacy.Allows(key, append(prior, layer.Marker)...) {
			return Invalid(field+".marker", "同一剖面的标志层名称不能重复")
		}
		seen[key] = append(prior, layer.Marker)
	}
	return nil
}
