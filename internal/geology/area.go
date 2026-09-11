package geology

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

const (
	AreaPrefix      = "area_"
	DefaultAreaName = "默认研究区"

	MaxAreaNameLength = 120
	MaxAreas          = 64

	// MaxProfiles 是整库剖面上限，同时也是单个研究区剖面配额的最大值。
	MaxProfiles = 2000
	// MaxAreaVersions 是单个研究区所有剖面版本总数的最大值。
	// 2000 个剖面、每个 500 版本的理论上限恰好为 1000000。
	MaxAreaVersions = 1_000_000
)

// DefaultAreaID 由固定字符串派生，保证任何进程、任何时间算出的编号一致。
var DefaultAreaID = newDefaultAreaID()

func newDefaultAreaID() string {
	sum := sha256.Sum256([]byte("default"))
	return AreaPrefix + hex.EncodeToString(sum[:16])
}

func ValidAreaID(id string) bool { return ValidID(id, AreaPrefix) }

type Area struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	MaxProfiles int    `json:"max_profiles"`
	MaxVersions int    `json:"max_versions"`
}

// NormalizeArea 校验研究区名称与配额，返回不含编号的规范化记录。
func NormalizeArea(name string, maxProfiles, maxVersions int) (Area, error) {
	name = strings.TrimSpace(name)
	if err := Text("name", name, 1, MaxAreaNameLength); err != nil {
		return Area{}, err
	}
	if maxProfiles < 1 || maxProfiles > MaxProfiles {
		return Area{}, Invalid("max_profiles", "剖面配额必须介于 1 和 2000 之间")
	}
	if maxVersions < 1 || maxVersions > MaxAreaVersions {
		return Area{}, Invalid("max_versions", "版本配额必须介于 1 和 1000000 之间")
	}
	return Area{Name: name, MaxProfiles: maxProfiles, MaxVersions: maxVersions}, nil
}

func (a Area) Validate() error {
	if !ValidAreaID(a.ID) {
		return Invalid("id", "研究区编号无效")
	}
	if a.ID == DefaultAreaID && (a.MaxProfiles != MaxProfiles || a.MaxVersions != MaxAreaVersions) {
		return Invalid("area", "默认研究区配额不能调整")
	}
	if _, err := NormalizeArea(a.Name, a.MaxProfiles, a.MaxVersions); err != nil {
		return err
	}
	return nil
}
