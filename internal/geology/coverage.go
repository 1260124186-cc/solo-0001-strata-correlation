package geology

type Interval struct {
	TopMM    int64 `json:"top_mm"`
	BottomMM int64 `json:"bottom_mm"`
}

type RockThickness struct {
	Rock        Lithology `json:"rock"`
	ThicknessMM int64     `json:"thickness_mm"`
}

type Coverage struct {
	DepthMM     int64           `json:"depth_mm"`
	CoveredMM   int64           `json:"covered_mm"`
	UnknownMM   int64           `json:"unknown_mm"`
	Gaps        []Interval      `json:"gaps"`
	Composition []RockThickness `json:"composition"`
	Ready       bool            `json:"ready"`
}

func CoverageOf(p Profile) Coverage {
	result := Coverage{DepthMM: p.DepthMM, Gaps: []Interval{}, Composition: []RockThickness{}}
	thickness := make(map[Lithology]int64)
	var end int64
	for _, layer := range p.Layers {
		if layer.TopMM > end {
			result.Gaps = append(result.Gaps, Interval{end, layer.TopMM})
		}
		n := layer.BottomMM - layer.TopMM
		thickness[layer.Rock] += n
		result.CoveredMM += n
		if layer.Rock == Unknown {
			result.UnknownMM += n
		}
		end = layer.BottomMM
	}
	if end < p.DepthMM {
		result.Gaps = append(result.Gaps, Interval{end, p.DepthMM})
	}
	for _, rock := range Rocks() {
		if thickness[rock.Code] > 0 {
			result.Composition = append(result.Composition, RockThickness{rock.Code, thickness[rock.Code]})
		}
	}
	result.Ready = len(result.Gaps) == 0
	return result
}
