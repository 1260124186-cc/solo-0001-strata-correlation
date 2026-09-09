package geology

import (
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

type State string

const (
	Draft     State = "draft"
	Sealed    State = "sealed"
	MaxDepth  int64 = 1000000
	MaxLayers       = 500
)

type Metadata struct {
	Name    string `json:"name"`
	Site    string `json:"site"`
	DepthMM int64  `json:"depth_mm"`
	Note    string `json:"note"`
}

type Profile struct {
	ID string `json:"id"`
	Metadata
	Layers    []Layer   `json:"layers"`
	State     State     `json:"state"`
	Version   int       `json:"version"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func Text(field, value string, min, max int) error {
	if !utf8.ValidString(value) {
		return Invalid(field, "必须为有效 UTF-8")
	}
	n := utf8.RuneCountInString(value)
	if n < min || n > max {
		return Invalid(field, "字符长度超出允许范围")
	}
	for _, r := range value {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return Invalid(field, "不能包含控制字符")
		}
	}
	return nil
}

func NormalizeMetadata(m Metadata) (Metadata, error) {
	m.Name = strings.TrimSpace(m.Name)
	m.Site = strings.TrimSpace(m.Site)
	m.Note = strings.TrimSpace(m.Note)
	for _, f := range []struct {
		name, value string
		min, max    int
	}{
		{"name", m.Name, 1, 120}, {"site", m.Site, 1, 200}, {"note", m.Note, 0, 2000},
	} {
		if err := Text(f.name, f.value, f.min, f.max); err != nil {
			return m, err
		}
	}
	if m.DepthMM <= 0 || m.DepthMM > MaxDepth {
		return m, Invalid("depth_mm", "必须介于 1 和 1000000 毫米之间")
	}
	return m, nil
}

func (p Profile) Clone() Profile {
	p.Layers = append([]Layer{}, p.Layers...)
	return p
}

func (p Profile) Validate() error {
	if !ValidID(p.ID, "prf_") {
		return Invalid("id", "剖面编号无效")
	}
	normalized, err := NormalizeMetadata(p.Metadata)
	if err != nil {
		return err
	}
	if normalized != p.Metadata {
		return Invalid("metadata", "元数据未规范化")
	}
	if p.Version < 1 {
		return Invalid("version", "版本必须为正整数")
	}
	if p.State != Draft && p.State != Sealed {
		return Invalid("state", "未知状态")
	}
	if p.CreatedAt.IsZero() || p.UpdatedAt.Before(p.CreatedAt) {
		return Invalid("time", "时间顺序无效")
	}
	if err := ValidateLayers(p.Layers, p.DepthMM); err != nil {
		return err
	}
	if p.State == Sealed && len(CoverageOf(p).Gaps) != 0 {
		return Invalid("layers", "锁定版本必须覆盖完整深度")
	}
	return nil
}

func ValidID(id, prefix string) bool {
	if !strings.HasPrefix(id, prefix) || len(id) != len(prefix)+32 {
		return false
	}
	for _, c := range id[len(prefix):] {
		if !(c >= '0' && c <= '9') && !(c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
