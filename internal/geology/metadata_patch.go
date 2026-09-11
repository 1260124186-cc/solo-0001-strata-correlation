package geology

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// MetadataPatch 表达元数据的局部更新：只修改显式出现的字段。
// 字段为 nil 表示请求中未出现，合并时保持原值；非 nil 表示显式提交，
// note 允许显式置为空串以清空说明。
type MetadataPatch struct {
	Name    *string `json:"-"`
	Site    *string `json:"-"`
	DepthMM *int64  `json:"-"`
	Note    *string `json:"-"`
}

func (m *MetadataPatch) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return Invalid("metadata", "局部元数据必须是 JSON 对象")
	}
	known := map[string]bool{"name": true, "site": true, "depth_mm": true, "note": true}
	for key := range raw {
		if !known[key] {
			return Invalid(key, "未知字段")
		}
	}
	decode := func(key string, dst any) error {
		segment, ok := raw[key]
		if !ok {
			return nil
		}
		if bytes.Equal(bytes.TrimSpace(segment), []byte("null")) {
			return Invalid(key, "字段不能为 null，省略字段表示保持原值")
		}
		if err := json.Unmarshal(segment, dst); err != nil {
			return Invalid(key, "字段类型不正确")
		}
		return nil
	}
	if err := decode("name", &m.Name); err != nil {
		return err
	}
	if err := decode("site", &m.Site); err != nil {
		return err
	}
	if err := decode("depth_mm", &m.DepthMM); err != nil {
		return err
	}
	return decode("note", &m.Note)
}

// HasFields 返回请求中显式出现的字段数。
func (m MetadataPatch) HasFields() int {
	count := 0
	if m.Name != nil {
		count++
	}
	if m.Site != nil {
		count++
	}
	if m.DepthMM != nil {
		count++
	}
	if m.Note != nil {
		count++
	}
	return count
}

// Merge 把补丁叠加到 base 上；未出现的字段保持原值。
// 结果尚未规范化，调用方仍需通过 NormalizeMetadata 统一校验。
func (m MetadataPatch) Merge(base Metadata) Metadata {
	merged := base
	if m.Name != nil {
		merged.Name = *m.Name
	}
	if m.Site != nil {
		merged.Site = *m.Site
	}
	if m.DepthMM != nil {
		merged.DepthMM = *m.DepthMM
	}
	if m.Note != nil {
		merged.Note = *m.Note
	}
	return merged
}

// MetadataChanges 按固定顺序返回两份元数据之间发生变化的字段。
// 顺序固定便于事件展示和快照一致性校验。
func MetadataChanges(before, after Profile) []FieldChange {
	fields := []FieldChange{
		{"name", before.Name, after.Name},
		{"site", before.Site, after.Site},
		{"note", before.Note, after.Note},
		{"depth_mm", fmt.Sprint(before.DepthMM), fmt.Sprint(after.DepthMM)},
	}
	changes := []FieldChange{}
	for _, field := range fields {
		if field.Before != field.After {
			changes = append(changes, field)
		}
	}
	return changes
}
