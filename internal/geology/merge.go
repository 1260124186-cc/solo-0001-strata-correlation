package geology

import (
	"fmt"
	"strconv"
)

// FieldConflict 表示基点之后主线与分叉线对同一字段给出了不同修改。
type FieldConflict struct {
	Field  string `json:"field"`
	Base   string `json:"base"`
	Main   string `json:"main"`
	Branch string `json:"branch"`
}

// LayerConflict 表示同一深度区间被两侧分别修改，或区间在两侧发生了不一致的删除/移动。
type LayerConflict struct {
	TopMM    int64  `json:"top_mm"`
	BottomMM int64  `json:"bottom_mm"`
	Base     *Layer `json:"base,omitempty"`
	Main     *Layer `json:"main,omitempty"`
	Branch   *Layer `json:"branch,omitempty"`
}

// MergeReport 是三向合并的逐项结果。MainChanges/BranchChanges 是相对基点的差异，
// Conflicts 列出无法自动合并的项；无冲突且几何校验通过时 Merged 为合并后的草拟剖面。
type MergeReport struct {
	MainChanges    Difference      `json:"main_changes"`
	BranchChanges  Difference      `json:"branch_changes"`
	FieldConflicts []FieldConflict `json:"field_conflicts"`
	LayerConflicts []LayerConflict `json:"layer_conflicts"`
	// BlockReason 描述逐项之外的阻塞，例如双方分层移动后区间重叠、合并深度越界。
	BlockReason string   `json:"block_reason,omitempty"`
	Merged      *Profile `json:"merged,omitempty"`
}

func (r *MergeReport) HasConflict() bool {
	return len(r.FieldConflicts) > 0 || len(r.LayerConflicts) > 0 || r.BlockReason != ""
}

type fieldValue struct {
	name                          string
	baseValue, mainValue, brValue string
}

type span struct{ top, bottom int64 }

func layerSpan(l Layer) span { return span{l.TopMM, l.BottomMM} }

func layerMap(p Profile) map[span]Layer {
	out := make(map[span]Layer, len(p.Layers))
	for _, layer := range p.Layers {
		out[layerSpan(layer)] = layer
	}
	return out
}

func layerPointer(l Layer) *Layer {
	copy := l
	return &copy
}

// ThreeWayMerge 以 base 为共同基点，将分叉线 head 相对主线 head 的修改合并。
// 仅一方修改或双方一致修改自动采纳；双方对同一字段/同一区间给出不同修改则记为冲突。
// 合并结果不重新编号、不改状态，交由调用方生成新版本。
func ThreeWayMerge(base, main, branch Profile) (MergeReport, error) {
	if base.ID != main.ID || base.ID != branch.ID {
		return MergeReport{}, Invalid("profile", "合并必须属于同一剖面")
	}
	report := MergeReport{
		MainChanges:    ContentDifference(base, main),
		BranchChanges:  ContentDifference(base, branch),
		FieldConflicts: []FieldConflict{},
		LayerConflicts: []LayerConflict{},
	}
	fields := []fieldValue{
		{"name", base.Name, main.Name, branch.Name},
		{"site", base.Site, main.Site, branch.Site},
		{"note", base.Note, main.Note, branch.Note},
		{"depth_mm", fmt.Sprint(base.DepthMM), fmt.Sprint(main.DepthMM), fmt.Sprint(branch.DepthMM)},
	}
	merged := main.Clone()
	for _, f := range fields {
		mainChanged, branchChanged := f.mainValue != f.baseValue, f.brValue != f.baseValue
		switch {
		case !mainChanged && branchChanged:
			applyField(&merged, f.name, f.brValue)
		case mainChanged && branchChanged && f.mainValue != f.brValue:
			report.FieldConflicts = append(report.FieldConflicts, FieldConflict{
				Field: f.name, Base: f.baseValue, Main: f.mainValue, Branch: f.brValue,
			})
		}
	}

	baseLayers, mainLayers, brLayers := layerMap(base), layerMap(main), layerMap(branch)
	allSpans := make(map[span]bool)
	for key := range baseLayers {
		allSpans[key] = true
	}
	for key := range mainLayers {
		allSpans[key] = true
	}
	for key := range brLayers {
		allSpans[key] = true
	}

	resultLayers := make(map[span]Layer)
	for key := range allSpans {
		b, hadBase := baseLayers[key]
		m, hasMain := mainLayers[key]
		br, hasBranch := brLayers[key]
		mainChanged := hasMain && (!hadBase || m != b) || !hasMain && hadBase
		branchChanged := hasBranch && (!hadBase || br != b) || !hasBranch && hadBase
		switch {
		case hasMain && hasBranch:
			switch {
			case mainChanged && branchChanged && m != br:
				conflict := LayerConflict{TopMM: key.top, BottomMM: key.bottom, Main: layerPointer(m), Branch: layerPointer(br)}
				if hadBase {
					conflict.Base = layerPointer(b)
				}
				report.LayerConflicts = append(report.LayerConflicts, conflict)
			case branchChanged && !mainChanged:
				resultLayers[key] = br // 仅分叉线修改
			default:
				resultLayers[key] = m // 主线修改、双方一致或均未修改
			}
		case hasMain && !hasBranch:
			switch {
			case hadBase && branchChanged && mainChanged:
				// 分叉线删除、主线修改同一区间。
				report.LayerConflicts = append(report.LayerConflicts, LayerConflict{TopMM: key.top, BottomMM: key.bottom, Base: layerPointer(b), Main: layerPointer(m)})
			case !hadBase:
				resultLayers[key] = m // 主线新增，分叉线无意见
			}
			// 基点存在且分叉线删除、主线未改：采纳删除。
		case !hasMain && hasBranch:
			switch {
			case hadBase && mainChanged && branchChanged:
				// 主线删除、分叉线修改同一区间。
				report.LayerConflicts = append(report.LayerConflicts, LayerConflict{TopMM: key.top, BottomMM: key.bottom, Base: layerPointer(b), Branch: layerPointer(br)})
			case !hadBase:
				resultLayers[key] = br // 分叉线新增
			}
			// 基点存在且主线删除、分叉线未改：采纳主线删除；双方均删除同样落入此处。
		}
	}
	merged.Layers = make([]Layer, 0, len(resultLayers))
	for _, layer := range resultLayers {
		merged.Layers = append(merged.Layers, layer)
	}
	layers, err := NormalizeLayers(merged.Layers, merged.DepthMM)
	if err == nil {
		merged.Layers = layers
	}

	if !report.HasConflict() && err == nil {
		merged.State = Draft
		report.Merged = &merged
	} else if err != nil && len(report.LayerConflicts) == 0 {
		// 双方各自移动边界等情况没有逐区间冲突，但合并结果不满足分层规则。
		report.BlockReason = "合并后的分层无法通过校验：" + err.Error()
	}
	return report, nil
}

func applyField(p *Profile, field, value string) {
	switch field {
	case "name":
		p.Name = value
	case "site":
		p.Site = value
	case "note":
		p.Note = value
	case "depth_mm":
		if depth, convErr := strconv.ParseInt(value, 10, 64); convErr == nil {
			p.DepthMM = depth
		}
	}
}
