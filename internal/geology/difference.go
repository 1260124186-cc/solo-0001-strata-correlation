package geology

import (
	"fmt"
	"sort"
)

type FieldChange struct {
	Field  string `json:"field"`
	Before string `json:"before"`
	After  string `json:"after"`
}

type LayerChange struct {
	TopMM    int64  `json:"top_mm"`
	BottomMM int64  `json:"bottom_mm"`
	Before   *Layer `json:"before"`
	After    *Layer `json:"after"`
}

type Difference struct {
	ProfileID   string        `json:"profile_id"`
	FromVersion int           `json:"from_version"`
	ToVersion   int           `json:"to_version"`
	Fields      []FieldChange `json:"fields"`
	Layers      []LayerChange `json:"layers"`
}

// MatchedLayer 是深度区间完全相同、可以一一对应的层对。
type MatchedLayer struct {
	TopMM    int64         `json:"top_mm"`
	BottomMM int64         `json:"bottom_mm"`
	Changes  []FieldChange `json:"changes"`
}

// DetailedDifference 把配对的层展开为字段级变化，
// 真正新增、删除或边界移动的层仍按区间变化表达。
type DetailedDifference struct {
	ProfileID   string         `json:"profile_id"`
	FromVersion int            `json:"from_version"`
	ToVersion   int            `json:"to_version"`
	Fields      []FieldChange  `json:"fields"`
	Layers      []LayerChange  `json:"layers"`
	Matched     []MatchedLayer `json:"matched"`
}

// spanDiff 是同一深度区间在两个版本间的对照，始终按版本升序记录。
type spanDiff struct {
	top    int64
	bottom int64
	before *Layer
	after  *Layer
}

// oriented 把任意方向的两个版本规范为时间顺序；
// reversed 为 true 表示请求方向是从新版本回看旧版本。
func oriented(from, to Profile) (older, newer Profile, reversed bool, err error) {
	if from.ID != to.ID {
		return Profile{}, Profile{}, false, Invalid("profile", "修订差异必须属于同一剖面")
	}
	switch {
	case from.Version == to.Version:
		return Profile{}, Profile{}, false, Invalid("version", "开始和结束版本不能相同")
	case from.Version < to.Version:
		return from, to, false, nil
	default:
		return to, from, true, nil
	}
}

func profileFields(older, newer Profile) []FieldChange {
	fields := []FieldChange{
		{"name", older.Name, newer.Name},
		{"site", older.Site, newer.Site},
		{"note", older.Note, newer.Note},
		{"depth_mm", fmt.Sprint(older.DepthMM), fmt.Sprint(newer.DepthMM)},
		{"state", string(older.State), string(newer.State)},
	}
	result := []FieldChange{}
	for _, field := range fields {
		if field.Before != field.After {
			result = append(result, field)
		}
	}
	return result
}

func layerFields(older, newer Layer) []FieldChange {
	fields := []FieldChange{
		{"rock", string(older.Rock), string(newer.Rock)},
		{"description", older.Description, newer.Description},
		{"marker", older.Marker, newer.Marker},
	}
	result := []FieldChange{}
	for _, field := range fields {
		if field.Before != field.After {
			result = append(result, field)
		}
	}
	return result
}

func spanDiffs(older, newer Profile) []spanDiff {
	type span struct{ top, bottom int64 }
	oldLayers := make(map[span]Layer)
	newLayers := make(map[span]Layer)
	spans := make(map[span]bool)
	for _, layer := range older.Layers {
		key := span{layer.TopMM, layer.BottomMM}
		oldLayers[key] = layer
		spans[key] = true
	}
	for _, layer := range newer.Layers {
		key := span{layer.TopMM, layer.BottomMM}
		newLayers[key] = layer
		spans[key] = true
	}
	var result []spanDiff
	for key := range spans {
		oldLayer, hadOld := oldLayers[key]
		newLayer, hasNew := newLayers[key]
		if hadOld && hasNew && oldLayer == newLayer {
			continue
		}
		entry := spanDiff{top: key.top, bottom: key.bottom}
		if hadOld {
			layer := oldLayer
			entry.before = &layer
		}
		if hasNew {
			layer := newLayer
			entry.after = &layer
		}
		result = append(result, entry)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].top != result[j].top {
			return result[i].top < result[j].top
		}
		return result[i].bottom < result[j].bottom
	})
	return result
}

func flipFields(fields []FieldChange) {
	for i := range fields {
		fields[i].Before, fields[i].After = fields[i].After, fields[i].Before
	}
}

func flipLayers(layers []LayerChange) {
	for i := range layers {
		layers[i].Before, layers[i].After = layers[i].After, layers[i].Before
	}
}

// DifferenceOf 计算同一剖面从 from 版本到 to 版本的差异。
// 两个版本可以按任意时间顺序给出：内部始终按版本升序计算一次，
// 再按请求方向交换前后值，同一处改动在两个方向上结论一致。
func DifferenceOf(from, to Profile) (Difference, error) {
	older, newer, reversed, err := oriented(from, to)
	if err != nil {
		return Difference{}, err
	}
	result := Difference{
		ProfileID:   from.ID,
		FromVersion: from.Version,
		ToVersion:   to.Version,
		Fields:      profileFields(older, newer),
		Layers:      []LayerChange{},
	}
	for _, span := range spanDiffs(older, newer) {
		result.Layers = append(result.Layers, LayerChange{TopMM: span.top, BottomMM: span.bottom, Before: span.before, After: span.after})
	}
	if reversed {
		flipFields(result.Fields)
		flipLayers(result.Layers)
	}
	return result, nil
}

// DetailedDifferenceOf 与 DifferenceOf 使用同一份已保存历史，
// 额外把深度区间完全相同的层对展开为字段级变化。
func DetailedDifferenceOf(from, to Profile) (DetailedDifference, error) {
	older, newer, reversed, err := oriented(from, to)
	if err != nil {
		return DetailedDifference{}, err
	}
	result := DetailedDifference{
		ProfileID:   from.ID,
		FromVersion: from.Version,
		ToVersion:   to.Version,
		Fields:      profileFields(older, newer),
		Layers:      []LayerChange{},
		Matched:     []MatchedLayer{},
	}
	for _, span := range spanDiffs(older, newer) {
		if span.before != nil && span.after != nil {
			result.Matched = append(result.Matched, MatchedLayer{
				TopMM:    span.top,
				BottomMM: span.bottom,
				Changes:  layerFields(*span.before, *span.after),
			})
			continue
		}
		result.Layers = append(result.Layers, LayerChange{TopMM: span.top, BottomMM: span.bottom, Before: span.before, After: span.after})
	}
	if reversed {
		flipFields(result.Fields)
		flipLayers(result.Layers)
		for i := range result.Matched {
			flipFields(result.Matched[i].Changes)
		}
	}
	return result, nil
}
