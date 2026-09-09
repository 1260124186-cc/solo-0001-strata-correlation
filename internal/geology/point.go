package geology

import "sort"

type Point struct {
	ProfileID          string    `json:"profile_id"`
	Version            int       `json:"version"`
	DepthMM            int64     `json:"depth_mm"`
	Layer              *Layer    `json:"layer"`
	Gap                *Interval `json:"gap"`
	DistanceFromTopMM  int64     `json:"distance_from_top_mm"`
	DistanceToBottomMM int64     `json:"distance_to_bottom_mm"`
}

func AtDepth(p Profile, depth int64) (Point, error) {
	if depth < 0 || depth >= p.DepthMM {
		return Point{}, Invalid("depth_mm", "深度必须位于剖面内，底端点不包含在区间中")
	}
	result := Point{ProfileID: p.ID, Version: p.Version, DepthMM: depth}
	index := sort.Search(len(p.Layers), func(i int) bool { return p.Layers[i].BottomMM > depth })
	if index < len(p.Layers) && p.Layers[index].TopMM <= depth {
		layer := p.Layers[index]
		result.Layer = &layer
		result.DistanceFromTopMM = depth - layer.TopMM
		result.DistanceToBottomMM = layer.BottomMM - depth
		return result, nil
	}
	top := int64(0)
	if index > 0 {
		top = p.Layers[index-1].BottomMM
	}
	bottom := p.DepthMM
	if index < len(p.Layers) {
		bottom = p.Layers[index].TopMM
	}
	result.Gap = &Interval{TopMM: top, BottomMM: bottom}
	result.DistanceFromTopMM = depth - top
	result.DistanceToBottomMM = bottom - depth
	return result, nil
}
