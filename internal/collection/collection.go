package collection

import (
	"slices"
	"strings"
	"time"

	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/geology"
)

const (
	MaxCollections = 500
	MaxMembers     = 200
)

// Member 绑定一个已经锁定的历史版本；历史版本不可变，绑定不会随剖面修订漂移。
type Member struct {
	ProfileID string `json:"profile_id"`
	Version   int    `json:"version"`
	Purpose   string `json:"purpose"`
}

// Collection 是同一地质专题下有序的一组锁定剖面版本。
type Collection struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Members     []Member  `json:"members"`
	Version     int       `json:"version"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// Normalize 修剪文本字段并校验名称、说明和成员数组的形状。
func Normalize(name, description string, members []Member) (string, string, []Member, error) {
	name = strings.TrimSpace(name)
	description = strings.TrimSpace(description)
	if err := geology.Text("name", name, 1, 120); err != nil {
		return "", "", nil, err
	}
	if err := geology.Text("description", description, 0, 2000); err != nil {
		return "", "", nil, err
	}
	if members == nil {
		return "", "", nil, geology.Invalid("members", "必须提供成员数组")
	}
	if len(members) < 1 || len(members) > MaxMembers {
		return "", "", nil, geology.Invalid("members", "成员数量必须为 1 到 200")
	}
	normalized := make([]Member, len(members))
	seen := make(map[Member]bool, len(members))
	for i, m := range members {
		m.Purpose = strings.TrimSpace(m.Purpose)
		if !geology.ValidID(m.ProfileID, "prf_") {
			return "", "", nil, geology.Invalid("profile_id", "剖面编号无效")
		}
		if m.Version < 1 {
			return "", "", nil, geology.Invalid("version", "需要指定正整数历史版本")
		}
		if err := geology.Text("purpose", m.Purpose, 1, 500); err != nil {
			return "", "", nil, err
		}
		key := Member{ProfileID: m.ProfileID, Version: m.Version}
		if seen[key] {
			return "", "", nil, geology.Invalid("members", "同一历史版本不能重复加入集合")
		}
		seen[key] = true
		normalized[i] = m
	}
	return name, description, normalized, nil
}

func (c Collection) Clone() Collection {
	c.Members = append([]Member{}, c.Members...)
	return c
}

func (c Collection) Validate() error {
	if !geology.ValidID(c.ID, "col_") {
		return geology.Invalid("id", "集合编号无效")
	}
	name, description, members, err := Normalize(c.Name, c.Description, c.Members)
	if err != nil {
		return err
	}
	if name != c.Name || description != c.Description || !slices.Equal(members, c.Members) {
		return geology.Invalid("collection", "集合内容未规范化")
	}
	if c.Version < 1 {
		return geology.Invalid("version", "版本必须为正整数")
	}
	if c.CreatedAt.IsZero() || c.UpdatedAt.Before(c.CreatedAt) {
		return geology.Invalid("time", "时间顺序无效")
	}
	return nil
}
