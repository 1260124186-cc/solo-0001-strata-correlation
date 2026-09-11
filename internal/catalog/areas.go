package catalog

import (
	"context"
	"fmt"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/geology"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/persistence"
	"sort"
	"strings"
)

func foldAreaName(name string) string { return strings.ToLower(strings.TrimSpace(name)) }

// CreateArea 是登记研究区的请求。
type CreateArea struct {
	Name        string `json:"name"`
	MaxProfiles int    `json:"max_profiles"`
	MaxVersions int    `json:"max_versions"`
}

// AreaStatus 是研究区归属与配额的实时统计，全部从持久化状态派生。
type AreaStatus struct {
	geology.Area
	UsedProfiles int      `json:"used_profiles"`
	UsedVersions int      `json:"used_versions"`
	FreeProfiles int      `json:"free_profiles"`
	FreeVersions int      `json:"free_versions"`
	ProfileIDs   []string `json:"profile_ids"`
}

// GlobalUsage 是仍然生效的整库上限及当前用量。
type GlobalUsage struct {
	ProfileLimit     int `json:"profile_limit"`
	ProfilesUsed     int `json:"profiles_used"`
	ProfilesFree     int `json:"profiles_free"`
	ComparisonLimit  int `json:"comparison_limit"`
	ComparisonsUsed  int `json:"comparisons_used"`
	ComparisonsFree  int `json:"comparisons_free"`
	TotalVersions    int `json:"total_versions"`
	SnapshotLimitMiB int `json:"snapshot_limit_mib"`
}

// AreaList 是研究区列表，末尾附整库用量，方便对照两套限制。
type AreaList struct {
	Items  []AreaStatus `json:"items"`
	Total  int          `json:"total"`
	Global GlobalUsage  `json:"global"`
}

const (
	maxComparisons   = 10000
	snapshotLimitMiB = 64
	maxAreasCount    = geology.MaxAreas
)

func areaStatus(state *persistence.State, area geology.Area) AreaStatus {
	ids := make([]string, 0)
	versions := 0
	for id, owner := range state.Memberships {
		if owner == area.ID {
			ids = append(ids, id)
			versions += len(state.Histories[id])
		}
	}
	sort.Strings(ids)
	usedProfiles := len(ids)
	return AreaStatus{
		Area:         area,
		UsedProfiles: usedProfiles,
		UsedVersions: versions,
		FreeProfiles: area.MaxProfiles - usedProfiles,
		FreeVersions: area.MaxVersions - versions,
		ProfileIDs:   ids,
	}
}

func globalUsage(state *persistence.State) GlobalUsage {
	totalVersions := 0
	for _, history := range state.Histories {
		totalVersions += len(history)
	}
	comparisonsUsed := len(state.Comparisons)
	return GlobalUsage{
		ProfileLimit:     geology.MaxProfiles,
		ProfilesUsed:     len(state.Histories),
		ProfilesFree:     geology.MaxProfiles - len(state.Histories),
		ComparisonLimit:  maxComparisons,
		ComparisonsUsed:  comparisonsUsed,
		ComparisonsFree:  maxComparisons - comparisonsUsed,
		TotalVersions:    totalVersions,
		SnapshotLimitMiB: snapshotLimitMiB,
	}
}

func (s *Service) CreateArea(ctx context.Context, input CreateArea) (AreaStatus, error) {
	area, err := geology.NormalizeArea(input.Name, input.MaxProfiles, input.MaxVersions)
	if err != nil {
		return AreaStatus{}, err
	}
	id, err := newID(geology.AreaPrefix)
	if err != nil {
		return AreaStatus{}, err
	}
	area.ID = id
	var status AreaStatus
	err = s.repo.Update(ctx, func(state *persistence.State) (bool, error) {
		if len(state.Areas) >= maxAreasCount {
			return false, geology.Conflict(fmt.Sprintf("研究区数量达到 %d 个上限", maxAreasCount))
		}
		for _, existing := range state.Areas {
			if foldAreaName(existing.Name) == foldAreaName(area.Name) {
				return false, geology.Conflict("同名研究区已存在：" + existing.Name)
			}
		}
		if _, exists := state.Areas[id]; exists {
			return false, geology.Conflict("研究区编号重复，请重试")
		}
		state.Areas[id] = area
		status = areaStatus(state, area)
		return true, nil
	})
	return status, err
}

func (s *Service) Areas(ctx context.Context) (AreaList, error) {
	var list AreaList
	err := s.repo.View(ctx, func(state persistence.State) error {
		statuses := make([]AreaStatus, 0, len(state.Areas))
		for _, area := range state.Areas {
			statuses = append(statuses, areaStatus(&state, area))
		}
		sort.Slice(statuses, func(i, j int) bool {
			if statuses[i].ID != geology.DefaultAreaID && statuses[j].ID != geology.DefaultAreaID {
				return statuses[i].ID < statuses[j].ID
			}
			return statuses[i].ID == geology.DefaultAreaID
		})
		list = AreaList{Items: statuses, Total: len(statuses), Global: globalUsage(&state)}
		return nil
	})
	return list, err
}

func (s *Service) Area(ctx context.Context, id string) (AreaStatus, GlobalUsage, error) {
	var status AreaStatus
	var global GlobalUsage
	err := s.repo.View(ctx, func(state persistence.State) error {
		area, ok := state.Areas[id]
		if !ok {
			return geology.Missing("研究区不存在")
		}
		status = areaStatus(&state, area)
		global = globalUsage(&state)
		return nil
	})
	return status, global, err
}
