package geology

import "time"

// Derivation 记录一次剖面派生：从来源剖面的锁定历史版本截取
// [TopMM, BottomMM) 区间，创建新的草拟剖面。记录创建后不可变，
// 来源剖面的后续修订不会改写已保存的派生关系。
type Derivation struct {
	DerivedID     string    `json:"derived_id"`
	SourceID      string    `json:"source_id"`
	SourceVersion int       `json:"source_version"`
	TopMM         int64     `json:"top_mm"`
	BottomMM      int64     `json:"bottom_mm"`
	CreatedAt     time.Time `json:"created_at"`
}

// Crop 截取剖面 [topMM, bottomMM) 区间内的分层，并平移到以区间起点为零点的新深度。
// 边界穿过的分层按交集截断；标志层仅在所属分层完整落入区间时保留，
// 被截断的分层不携带标志层；区间内的覆盖缺口原样保留，不填补也不移除。
func Crop(p Profile, topMM, bottomMM int64) ([]Layer, error) {
	if topMM < 0 || bottomMM > p.DepthMM || topMM >= bottomMM {
		return nil, Invalid("range", "裁剪区间必须为正长度且位于剖面深度范围内")
	}
	layers := []Layer{}
	for _, layer := range p.Layers {
		top := max(layer.TopMM, topMM)
		bottom := min(layer.BottomMM, bottomMM)
		if top >= bottom {
			continue
		}
		cropped := Layer{
			TopMM:       top - topMM,
			BottomMM:    bottom - topMM,
			Rock:        layer.Rock,
			Description: layer.Description,
		}
		if layer.TopMM >= topMM && layer.BottomMM <= bottomMM {
			cropped.Marker = layer.Marker
		}
		layers = append(layers, cropped)
	}
	return layers, nil
}

// CheckDerivationLink 校验加入 derived←source 的派生关系不会形成循环：
// 来源的完整祖先链中不得包含派生物自身。
func CheckDerivationLink(derivations map[string]Derivation, derivedID, sourceID string) error {
	current := sourceID
	for steps := 0; steps <= len(derivations); steps++ {
		if current == derivedID {
			return Conflict("该派生会使剖面成为自己的祖先，已拒绝")
		}
		d, ok := derivations[current]
		if !ok {
			return nil
		}
		current = d.SourceID
	}
	return Conflict("派生关系存在循环，已拒绝")
}
