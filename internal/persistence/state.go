package persistence

import (
	"encoding/json"
	"fmt"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/correlation"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/geology"
	"reflect"
	"strings"
)

func foldName(name string) string { return strings.ToLower(strings.TrimSpace(name)) }

type State struct {
	Schema      int                           `json:"schema"`
	Areas       map[string]geology.Area       `json:"areas"`
	Memberships map[string]string             `json:"memberships"`
	Histories   map[string][]geology.Revision `json:"histories"`
	Comparisons map[string]correlation.Result `json:"comparisons"`
}

func defaultArea() geology.Area {
	return geology.Area{ID: geology.DefaultAreaID, Name: geology.DefaultAreaName, MaxProfiles: geology.MaxProfiles, MaxVersions: geology.MaxAreaVersions}
}

func emptyState() State {
	return State{
		Schema:      2,
		Areas:       map[string]geology.Area{geology.DefaultAreaID: defaultArea()},
		Memberships: map[string]string{},
		Histories:   map[string][]geology.Revision{},
		Comparisons: map[string]correlation.Result{},
	}
}

func (s State) Clone() State {
	out := emptyState()
	for id, area := range s.Areas {
		out.Areas[id] = area
	}
	for id, areaID := range s.Memberships {
		out.Memberships[id] = areaID
	}
	for id, revisions := range s.Histories {
		copies := make([]geology.Revision, len(revisions))
		for i, r := range revisions {
			copies[i] = r.Clone()
		}
		out.Histories[id] = copies
	}
	for id, result := range s.Comparisons {
		out.Comparisons[id] = result.Clone()
	}
	return out
}

func (s State) Latest(id string) (geology.Profile, error) {
	history, ok := s.Histories[id]
	if !ok || len(history) == 0 {
		return geology.Profile{}, geology.Missing("剖面不存在")
	}
	return history[len(history)-1].Profile.Clone(), nil
}

func (s State) Revision(id string, version int) (geology.Revision, error) {
	history, ok := s.Histories[id]
	if !ok {
		return geology.Revision{}, geology.Missing("剖面不存在")
	}
	if version < 1 || version > len(history) {
		return geology.Revision{}, geology.Missing("历史版本不存在")
	}
	return history[version-1].Clone(), nil
}

// ProfileCount 返回某研究区当前的剖面数量。
func (s State) ProfileCount(areaID string) int {
	count := 0
	for _, owner := range s.Memberships {
		if owner == areaID {
			count++
		}
	}
	return count
}

// VersionUsage 返回某研究区所有剖面历史版本总数。
func (s State) VersionUsage(areaID string) int {
	total := 0
	for id, owner := range s.Memberships {
		if owner == areaID {
			total += len(s.Histories[id])
		}
	}
	return total
}

// CheckCreateQuota 在同一已提交状态上依次检查研究区配额和整库上限。
// 研究区配额先判定；研究区有余量但整库已满时，错误信息同时给出两套数字。
// 新建剖面自带第 1 个版本，因此同时占用研究区的剖面配额与版本配额各一项。
func (s State) CheckCreateQuota(areaID string) error {
	if !geology.ValidAreaID(areaID) {
		return geology.Invalid("area_id", "研究区编号无效")
	}
	area, ok := s.Areas[areaID]
	if !ok {
		return geology.Invalid("area_id", "研究区不存在，请先登记研究区")
	}
	if used := s.ProfileCount(areaID); used >= area.MaxProfiles {
		return geology.Conflict(fmt.Sprintf(
			"研究区「%s」剖面配额已满：已用 %d / 上限 %d，剩余 0 个；请在该研究区配额范围内操作或联系管理员调整配额",
			area.Name, used, area.MaxProfiles))
	}
	if usedVersions := s.VersionUsage(areaID); usedVersions >= area.MaxVersions {
		return geology.Conflict(fmt.Sprintf(
			"研究区「%s」版本配额已满：已用 %d / 上限 %d，剩余 0 个版本；新剖面的首个版本也无法写入",
			area.Name, usedVersions, area.MaxVersions))
	}
	if len(s.Histories) >= geology.MaxProfiles {
		return geology.Conflict(fmt.Sprintf(
			"整库剖面上限已达 %d / %d（研究区「%s」尚有余量 %d 个）；整库容量由统一上限约束，请释放其他研究区的资料或联系管理员",
			len(s.Histories), geology.MaxProfiles, area.Name, area.MaxProfiles-s.ProfileCount(areaID)))
	}
	return nil
}

// CheckRevisionQuota 在同一已提交状态上检查某剖面下一次版本提交是否同时
// 满足研究区版本配额。整库层面没有独立的版本总数上限，由 64 MiB 快照上限兜底。
func (s State) CheckRevisionQuota(profileID string) error {
	areaID := s.Memberships[profileID]
	area, ok := s.Areas[areaID]
	if !ok {
		return fmt.Errorf("profile %s has no area ownership", profileID)
	}
	if used := s.VersionUsage(areaID); used >= area.MaxVersions {
		return geology.Conflict(fmt.Sprintf(
			"研究区「%s」版本配额已满：已用 %d / 上限 %d，剩余 0 个版本；该剖面不能再生成新版本",
			area.Name, used, area.MaxVersions))
	}
	return nil
}

func (s State) Validate() error {
	if s.Schema != 2 {
		return fmt.Errorf("unsupported snapshot shape")
	}
	if s.Areas == nil || s.Memberships == nil || s.Histories == nil || s.Comparisons == nil {
		return fmt.Errorf("unsupported snapshot shape")
	}
	defaultArea, ok := s.Areas[geology.DefaultAreaID]
	if !ok {
		return fmt.Errorf("default area missing")
	}
	if defaultArea.Name != geology.DefaultAreaName || defaultArea.MaxProfiles != geology.MaxProfiles || defaultArea.MaxVersions != geology.MaxAreaVersions {
		return fmt.Errorf("default area configuration changed")
	}
	names := map[string]string{}
	for id, area := range s.Areas {
		if err := area.Validate(); err != nil {
			return fmt.Errorf("invalid area %s: %w", id, err)
		}
		key := foldName(area.Name)
		if other, dup := names[key]; dup {
			return fmt.Errorf("duplicate area name %q (%s, %s)", area.Name, other, id)
		}
		names[key] = id
	}
	for id, history := range s.Histories {
		if len(history) == 0 {
			return fmt.Errorf("empty history %s", id)
		}
		areaID, owned := s.Memberships[id]
		if !owned {
			return fmt.Errorf("profile %s missing area ownership", id)
		}
		if _, exists := s.Areas[areaID]; !exists {
			return fmt.Errorf("profile %s belongs to unknown area %s", id, areaID)
		}
		for i, r := range history {
			if r.Profile.ID != id || r.Profile.Version != i+1 || r.Event.Version != i+1 || !r.Event.At.Equal(r.Profile.UpdatedAt) {
				return fmt.Errorf("inconsistent revision %s/%d", id, i+1)
			}
			if err := r.Profile.Validate(); err != nil {
				return fmt.Errorf("invalid revision %s: %w", id, err)
			}
			if err := geology.Text("reason", r.Event.Reason, 1, 500); err != nil {
				return err
			}
			if i == 0 {
				if r.Event.Action != "create" || r.Profile.State != geology.Draft {
					return fmt.Errorf("invalid initial revision")
				}
			} else {
				before := history[i-1].Profile
				if !before.CreatedAt.Equal(r.Profile.CreatedAt) || r.Profile.UpdatedAt.Before(before.UpdatedAt) {
					return fmt.Errorf("invalid revision chronology")
				}
				if err := validateStep(before, r); err != nil {
					return err
				}
			}
		}
	}
	for id := range s.Memberships {
		if _, exists := s.Histories[id]; !exists {
			return fmt.Errorf("area ownership %s without history", id)
		}
	}
	for id, result := range s.Comparisons {
		if id != result.ID || id != result.Request.Key() || result.Algorithm != correlation.Algorithm || result.CreatedAt.IsZero() {
			return fmt.Errorf("invalid comparison identity")
		}
		a, err := s.Revision(result.Request.Left.ID, result.Request.Left.Version)
		if err != nil {
			return err
		}
		b, err := s.Revision(result.Request.Right.ID, result.Request.Right.Version)
		if err != nil {
			return err
		}
		computed, err := correlation.Align(a.Profile, b.Profile, result.Request, result.CreatedAt)
		if err != nil {
			return err
		}
		expected, _ := json.Marshal(computed)
		actual, _ := json.Marshal(result)
		if string(expected) != string(actual) {
			return fmt.Errorf("comparison data mismatch")
		}
	}
	return nil
}

func validateStep(before geology.Profile, r geology.Revision) error {
	after := r.Profile
	switch r.Event.Action {
	case "metadata", "layers":
		if before.State != geology.Draft || after.State != geology.Draft {
			return fmt.Errorf("edited sealed revision")
		}
		if r.Event.Action == "layers" && before.Metadata != after.Metadata {
			return fmt.Errorf("layers edit changed metadata")
		}
		if r.Event.Action == "metadata" && !reflect.DeepEqual(before.Layers, after.Layers) {
			return fmt.Errorf("metadata edit changed layers")
		}
	case "seal", "reopen":
		expected := geology.Sealed
		if r.Event.Action == "reopen" {
			expected = geology.Draft
		}
		if after.State != expected || before.State == expected || before.Metadata != after.Metadata || !reflect.DeepEqual(before.Layers, after.Layers) {
			return fmt.Errorf("invalid state change")
		}
	default:
		return fmt.Errorf("unknown revision action")
	}
	return nil
}
