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

func DifferenceOf(before, after Profile) (Difference, error) {
	if before.ID != after.ID {
		return Difference{}, Invalid("profile", "修订差异必须属于同一剖面")
	}
	if before.Version >= after.Version {
		return Difference{}, Invalid("version", "结束版本必须大于开始版本")
	}
	result := Difference{ProfileID: before.ID, FromVersion: before.Version, ToVersion: after.Version, Fields: []FieldChange{}, Layers: []LayerChange{}}
	fields := []FieldChange{
		{"name", before.Name, after.Name},
		{"site", before.Site, after.Site},
		{"note", before.Note, after.Note},
		{"recorder", before.Recorder, after.Recorder},
		{"depth_mm", fmt.Sprint(before.DepthMM), fmt.Sprint(after.DepthMM)},
		{"state", string(before.State), string(after.State)},
	}
	for _, field := range fields {
		if field.Before != field.After {
			result.Fields = append(result.Fields, field)
		}
	}
	type span struct{ top, bottom int64 }
	oldLayers := make(map[span]Layer)
	newLayers := make(map[span]Layer)
	spans := make(map[span]bool)
	for _, layer := range before.Layers {
		key := span{layer.TopMM, layer.BottomMM}
		oldLayers[key] = layer
		spans[key] = true
	}
	for _, layer := range after.Layers {
		key := span{layer.TopMM, layer.BottomMM}
		newLayers[key] = layer
		spans[key] = true
	}
	for key := range spans {
		old, hadOld := oldLayers[key]
		next, hasNext := newLayers[key]
		if hadOld && hasNext && old == next {
			continue
		}
		change := LayerChange{TopMM: key.top, BottomMM: key.bottom}
		if hadOld {
			change.Before = &old
		}
		if hasNext {
			change.After = &next
		}
		result.Layers = append(result.Layers, change)
	}
	sort.Slice(result.Layers, func(i, j int) bool {
		if result.Layers[i].TopMM != result.Layers[j].TopMM {
			return result.Layers[i].TopMM < result.Layers[j].TopMM
		}
		return result.Layers[i].BottomMM < result.Layers[j].BottomMM
	})
	return result, nil
}
