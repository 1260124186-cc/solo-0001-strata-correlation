package geology

import "fmt"

// Side 选择在拆分或合并时保留相邻两层中的哪一侧。
type Side string

const (
	Upper Side = "upper"
	Lower Side = "lower"
)

func (s Side) valid() bool { return s == "" || s == Upper || s == Lower }

// chooseSide 统一处理“两侧一致时无需选择、不一致时必须选择”的规则。
func chooseSide(field string, same bool, side Side) error {
	if !side.valid() {
		return Invalid(field, "只能选择 upper 或 lower")
	}
	if same && side != "" {
		return Invalid(field, "两侧内容一致，不需要选择保留哪一侧")
	}
	if !same && side == "" {
		return Invalid(field, "两侧内容不一致，必须明确选择保留哪一侧")
	}
	return nil
}

// SplitLayers 在 top 所指分层内部的 at 深度处拆成两层。岩性与描述原样保留；
// 存在标志层时由 markerSide 指定归属，无标志层时不允许携带归属选择。
// 结果与整体替换走同一套规范化与校验。
func SplitLayers(in []Layer, depthMM, topMM, atMM int64, markerSide Side) ([]Layer, error) {
	if !markerSide.valid() {
		return nil, Invalid("marker_side", "只能选择 upper 或 lower")
	}
	index := -1
	for i := range in {
		if in[i].TopMM == topMM {
			index = i
			break
		}
	}
	if index < 0 {
		return nil, Invalid("top_mm", "拆分起点必须是某一已有分层的顶部深度")
	}
	original := in[index]
	if atMM <= original.TopMM || atMM >= original.BottomMM {
		return nil, Invalid("at_mm", "拆分深度必须严格位于分层内部")
	}
	if original.Marker == "" {
		if markerSide != "" {
			return nil, Invalid("marker_side", "该分层没有标志层，不需要选择归属")
		}
	} else if markerSide == "" {
		return nil, Invalid("marker_side", "标志层必须明确归入拆分后的上层或下层")
	}
	upper := Layer{TopMM: original.TopMM, BottomMM: atMM, Rock: original.Rock, Description: original.Description}
	lower := Layer{TopMM: atMM, BottomMM: original.BottomMM, Rock: original.Rock, Description: original.Description}
	if markerSide == Upper {
		upper.Marker = original.Marker
	} else if markerSide == Lower {
		lower.Marker = original.Marker
	}
	out := append([]Layer{}, in[:index]...)
	out = append(out, upper, lower)
	out = append(out, in[index+1:]...)
	return NormalizeLayers(out, depthMM)
}

// MergeChoice 描述合并两层时各字段保留哪一侧；仅在对应字段两侧不一致时才允许出现。
type MergeChoice struct {
	Rock        Side
	Description Side
	Marker      Side
}

// MergeLayers 合并在 boundary 深度处严格相邻（顶底相接、中间无缺口）的两层。
// 岩性、描述不一致时必须选择保留哪一侧；标志层仅在两层都带标志层时才需要选择。
// 结果与整体替换走同一套规范化与校验。
func MergeLayers(in []Layer, depthMM, boundaryMM int64, choice MergeChoice) ([]Layer, error) {
	for _, item := range []struct {
		field string
		side  Side
	}{
		{"rock", choice.Rock}, {"description", choice.Description}, {"marker", choice.Marker},
	} {
		if !item.side.valid() {
			return nil, Invalid(item.field, "只能选择 upper 或 lower")
		}
	}
	index := -1
	for i := 0; i+1 < len(in); i++ {
		if in[i].BottomMM == boundaryMM && in[i+1].TopMM == boundaryMM {
			index = i
			break
		}
	}
	if index < 0 {
		return nil, Invalid("boundary_mm", "只能合并共享边界且相邻的两层，跨缺口修订请使用整体替换")
	}
	up, low := in[index], in[index+1]
	if err := chooseSide("rock", up.Rock == low.Rock, choice.Rock); err != nil {
		return nil, err
	}
	if err := chooseSide("description", up.Description == low.Description, choice.Description); err != nil {
		return nil, err
	}
	// 同一剖面的标志层名称忽略大小写唯一，因此两层都有标志层时必然不一致。
	markerConflict := up.Marker != "" && low.Marker != ""
	if err := chooseSide("marker", !markerConflict, choice.Marker); err != nil {
		return nil, err
	}
	merged := Layer{TopMM: up.TopMM, BottomMM: low.BottomMM}
	merged.Rock = up.Rock
	if choice.Rock == Lower {
		merged.Rock = low.Rock
	}
	merged.Description = up.Description
	if choice.Description == Lower {
		merged.Description = low.Description
	}
	switch {
	case markerConflict && choice.Marker == Lower:
		merged.Marker = low.Marker
	case markerConflict:
		merged.Marker = up.Marker
	case up.Marker != "":
		merged.Marker = up.Marker
	default:
		merged.Marker = low.Marker
	}
	out := append([]Layer{}, in[:index]...)
	out = append(out, merged)
	out = append(out, in[index+2:]...)
	return NormalizeLayers(out, depthMM)
}

// ValidateSplitStep 供持久化启动校验使用：确认 after 恰好是 before 中一层拆成两层，
// 岩性和描述被原样保留，标志层只可能落在其中一侧，其余分层完全不变。
func ValidateSplitStep(before, after []Layer) error {
	if len(after) != len(before)+1 {
		return fmt.Errorf("split revision must add exactly one layer")
	}
	i := 0
	for i < len(before) && before[i] == after[i] {
		i++
	}
	if i >= len(before) {
		return fmt.Errorf("split revision did not change any layer")
	}
	original, upper, lower := before[i], after[i], after[i+1]
	if upper.TopMM != original.TopMM || lower.BottomMM != original.BottomMM ||
		upper.BottomMM != lower.TopMM || upper.BottomMM <= upper.TopMM {
		return fmt.Errorf("split revision changed layer bounds")
	}
	if upper.Rock != original.Rock || lower.Rock != original.Rock ||
		upper.Description != original.Description || lower.Description != original.Description {
		return fmt.Errorf("split revision must preserve rock and description")
	}
	switch original.Marker {
	case "":
		if upper.Marker != "" || lower.Marker != "" {
			return fmt.Errorf("split revision introduced a marker")
		}
	default:
		if (upper.Marker != original.Marker || lower.Marker != "") &&
			(lower.Marker != original.Marker || upper.Marker != "") {
			return fmt.Errorf("split revision must move the marker to exactly one side")
		}
	}
	for j := i + 1; j < len(before); j++ {
		if before[j] != after[j+1] {
			return fmt.Errorf("split revision changed unrelated layers")
		}
	}
	return nil
}

// ValidateMergeStep 供持久化启动校验使用：确认 after 恰好是 before 中相邻两层合并，
// 合并层的每个字段都取自原两层之一，其余分层完全不变。
func ValidateMergeStep(before, after []Layer) error {
	if len(before) < 2 || len(after) != len(before)-1 {
		return fmt.Errorf("merge revision must remove exactly one layer")
	}
	i := 0
	for i < len(after) && before[i] == after[i] {
		i++
	}
	if i >= len(after) {
		return fmt.Errorf("merge revision did not change any layer")
	}
	up, low, merged := before[i], before[i+1], after[i]
	if merged.TopMM != up.TopMM || merged.BottomMM != low.BottomMM || up.BottomMM != low.TopMM {
		return fmt.Errorf("merge revision changed layer bounds")
	}
	pairs := []struct {
		up, low, got string
	}{
		{string(up.Rock), string(low.Rock), string(merged.Rock)},
		{up.Description, low.Description, merged.Description},
		{up.Marker, low.Marker, merged.Marker},
	}
	for _, p := range pairs {
		if p.got != p.up && p.got != p.low {
			return fmt.Errorf("merge revision must keep one of each adjacent value")
		}
	}
	for j := i + 2; j < len(before); j++ {
		if before[j] != after[j-1] {
			return fmt.Errorf("merge revision changed unrelated layers")
		}
	}
	return nil
}
