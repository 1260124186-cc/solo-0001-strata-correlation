package geology

const (
	RangeLayer = "layer"
	RangeGap   = "gap"
)

type RangeItem struct {
	TopMM          int64     `json:"top_mm"`
	BottomMM       int64     `json:"bottom_mm"`
	Kind           string    `json:"kind"`
	Layer          *Layer    `json:"layer"`
	Gap            *Interval `json:"gap"`
	SourceTopMM    int64     `json:"source_top_mm"`
	SourceBottomMM int64     `json:"source_bottom_mm"`
}

type RangeResult struct {
	ProfileID string      `json:"profile_id"`
	Version   int         `json:"version"`
	DepthMM   int64       `json:"depth_mm"`
	FromMM    int64       `json:"from_mm"`
	ToMM      int64       `json:"to_mm"`
	Items     []RangeItem `json:"items"`
}

func RangeOf(p Profile, from, to int64) (RangeResult, error) {
	if from < 0 || to > p.DepthMM {
		return RangeResult{}, Invalid("range", "区间不能超出剖面范围，服务不会截断越界区间")
	}
	if to <= from {
		return RangeResult{}, Invalid("range", "区间必须是非空左闭右开区间")
	}
	result := RangeResult{
		ProfileID: p.ID,
		Version:   p.Version,
		DepthMM:   p.DepthMM,
		FromMM:    from,
		ToMM:      to,
		Items:     []RangeItem{},
	}
	for depth := from; depth < to; {
		layer, gap, found := spanAt(p, depth)
		item := RangeItem{TopMM: depth}
		if found {
			item.BottomMM = min(to, layer.BottomMM)
			item.Kind = RangeLayer
			layerCopy := layer
			item.Layer = &layerCopy
			item.SourceTopMM = layer.TopMM
			item.SourceBottomMM = layer.BottomMM
		} else {
			item.BottomMM = min(to, gap.BottomMM)
			item.Kind = RangeGap
			gapCopy := gap
			item.Gap = &gapCopy
			item.SourceTopMM = gap.TopMM
			item.SourceBottomMM = gap.BottomMM
		}
		result.Items = append(result.Items, item)
		depth = item.BottomMM
	}
	return result, nil
}
