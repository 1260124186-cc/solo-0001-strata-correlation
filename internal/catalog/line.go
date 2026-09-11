package catalog

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/geology"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/line"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/persistence"
	"sort"
	"strings"
	"time"
)

const maxLines = 1000

// StationInput 是创建或替换站点时客户端提供的内容。并列序号不接受输入，
// 由系统按距离与调整排序接口统一计算，避免与内部存储顺序耦合。
type StationInput struct {
	ProfileID  string `json:"profile_id"`
	Version    int    `json:"version"`
	ChainageMM int64  `json:"chainage_mm"`
}

type CreateLine struct {
	Name     string         `json:"name"`
	Note     string         `json:"note"`
	Stations []StationInput `json:"stations"`
}

type LineMeta struct {
	ExpectedRevision int    `json:"expected_revision"`
	Reason           string `json:"reason"`
	Name             string `json:"name"`
	Note             string `json:"note"`
}

type ReplaceLineStations struct {
	ExpectedRevision int            `json:"expected_revision"`
	Reason           string         `json:"reason"`
	Stations         []StationInput `json:"stations"`
}

type ReorderLine struct {
	ExpectedRevision int      `json:"expected_revision"`
	Reason           string   `json:"reason"`
	Order            []string `json:"order"`
}

type FinalizeLine struct {
	ExpectedRevision int    `json:"expected_revision"`
	Reason           string `json:"reason"`
}

type ForkLine struct {
	Name string `json:"name"`
	Note string `json:"note"`
}

func newLineID() (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return "line_" + hex.EncodeToString(bytes), nil
}

func normalizeLineText(name, note string) (string, string, error) {
	name = strings.TrimSpace(name)
	note = strings.TrimSpace(note)
	if err := geology.Text("name", name, 1, line.MaxNameLength); err != nil {
		return "", "", err
	}
	if err := geology.Text("note", note, 0, line.MaxNoteLength); err != nil {
		return "", "", err
	}
	return name, note, nil
}

// checkDraftRevision 强制执行草稿状态与乐观修订号。
func checkDraftRevision(l line.Line, expected int) error {
	if l.State != line.Draft {
		return geology.Conflict("测线已定稿，内容已冻结，需要新版本请基于它建立新草稿")
	}
	if l.Revision != expected {
		return geology.VersionConflict(expected, l.Revision)
	}
	return nil
}

// resolveStations 先规范化站点形状与并列顺序，再确认每个引用都指向存在且已锁定的版本。
// 缺失版本或引用非锁定版本都会被明确拒绝；此函数不写入任何状态。
func resolveStations(state *persistence.State, stations []line.Station) ([]line.Station, error) {
	canonical, err := line.Canonicalize(stations)
	if err != nil {
		return nil, err
	}
	for _, st := range canonical {
		revision, err := state.Revision(st.ProfileID, st.Version)
		if err != nil {
			return nil, geology.Invalid("stations", "位置引用的剖面版本不存在，请先锁定该版本")
		}
		if revision.Profile.State != geology.Sealed {
			return nil, geology.Conflict("位置只能绑定已锁定的剖面版本")
		}
	}
	return canonical, nil
}

func toStationInputs(in []StationInput) []line.Station {
	out := make([]line.Station, 0, len(in))
	for _, s := range in {
		out = append(out, line.Station{ProfileID: s.ProfileID, Version: s.Version, ChainageMM: s.ChainageMM})
	}
	return out
}

func optionalReason(reason string) (string, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return "", nil
	}
	return reason, geology.Text("reason", reason, 1, 500)
}

func (s *Service) CreateLine(ctx context.Context, input CreateLine) (line.Line, error) {
	name, note, err := normalizeLineText(input.Name, input.Note)
	if err != nil {
		return line.Line{}, err
	}
	if input.Stations == nil {
		input.Stations = []StationInput{} // 草稿允许为空，定稿时才要求至少两个位置
	}
	id, err := newLineID()
	if err != nil {
		return line.Line{}, err
	}
	now := time.Now().UTC()
	result := line.Line{ID: id, Name: name, Note: note, State: line.Draft, Stations: []line.Station{}, Revision: 1, CreatedAt: now, UpdatedAt: now}
	err = s.repo.Update(ctx, func(state *persistence.State) (bool, error) {
		if len(state.Lines) >= maxLines {
			return false, geology.Conflict("最多保存 1000 条测线")
		}
		if _, exists := state.Lines[id]; exists {
			return false, geology.Conflict("测线编号重复，请重试")
		}
		stations, err := resolveStations(state, toStationInputs(input.Stations))
		if err != nil {
			return false, err
		}
		result.Stations = stations
		if err := result.Validate(); err != nil {
			return false, err
		}
		state.Lines[id] = result.Clone()
		return true, nil
	})
	return result, err
}

func (s *Service) EditLineMeta(ctx context.Context, id string, input LineMeta) (line.Line, error) {
	name, note, err := normalizeLineText(input.Name, input.Note)
	if err != nil {
		return line.Line{}, err
	}
	if _, err = optionalReason(input.Reason); err != nil {
		return line.Line{}, err
	}
	if input.ExpectedRevision < 1 {
		return line.Line{}, geology.Invalid("expected_revision", "必须为正整数")
	}
	var result line.Line
	err = s.repo.Update(ctx, func(state *persistence.State) (bool, error) {
		return s.mutateLine(state, id, &result, func(l *line.Line, now time.Time) error {
			if err := checkDraftRevision(*l, input.ExpectedRevision); err != nil {
				return err
			}
			l.Name, l.Note = name, note
			return nil
		})
	})
	return result, err
}

func (s *Service) ReplaceLineStations(ctx context.Context, id string, input ReplaceLineStations) (line.Line, error) {
	if _, err := optionalReason(input.Reason); err != nil {
		return line.Line{}, err
	}
	if input.ExpectedRevision < 1 {
		return line.Line{}, geology.Invalid("expected_revision", "必须为正整数")
	}
	if input.Stations == nil {
		return line.Line{}, geology.Invalid("stations", "必须提供位置数组，清空时使用 []")
	}
	var result line.Line
	err := s.repo.Update(ctx, func(state *persistence.State) (bool, error) {
		return s.mutateLine(state, id, &result, func(l *line.Line, now time.Time) error {
			if err := checkDraftRevision(*l, input.ExpectedRevision); err != nil {
				return err
			}
			stations, err := resolveStations(state, toStationInputs(input.Stations))
			if err != nil {
				return err
			}
			l.Stations = stations
			return nil
		})
	})
	return result, err
}

func (s *Service) ReorderLine(ctx context.Context, id string, input ReorderLine) (line.Line, error) {
	if _, err := optionalReason(input.Reason); err != nil {
		return line.Line{}, err
	}
	if input.ExpectedRevision < 1 {
		return line.Line{}, geology.Invalid("expected_revision", "必须为正整数")
	}
	var result line.Line
	err := s.repo.Update(ctx, func(state *persistence.State) (bool, error) {
		return s.mutateLine(state, id, &result, func(l *line.Line, now time.Time) error {
			if err := checkDraftRevision(*l, input.ExpectedRevision); err != nil {
				return err
			}
			reordered, err := line.Reorder(l.Stations, input.Order)
			if err != nil {
				return err
			}
			l.Stations = reordered
			return nil
		})
	})
	return result, err
}

func (s *Service) FinalizeLine(ctx context.Context, id string, input FinalizeLine) (line.Line, error) {
	if _, err := normalizedReason(input.Reason); err != nil {
		return line.Line{}, err
	}
	if input.ExpectedRevision < 1 {
		return line.Line{}, geology.Invalid("expected_revision", "必须为正整数")
	}
	var result line.Line
	err := s.repo.Update(ctx, func(state *persistence.State) (bool, error) {
		return s.mutateLine(state, id, &result, func(l *line.Line, now time.Time) error {
			if err := checkDraftRevision(*l, input.ExpectedRevision); err != nil {
				return err
			}
			if len(l.Stations) < 2 {
				return geology.Conflict("至少需要两个位置才能定稿一条测线")
			}
			for _, st := range l.Stations {
				revision, err := state.Revision(st.ProfileID, st.Version)
				if err != nil {
					return geology.Invalid("stations", "存在缺失的剖面版本，不能定稿")
				}
				if revision.Profile.State != geology.Sealed {
					return geology.Conflict("存在未锁定的剖面版本，不能定稿")
				}
			}
			l.State = line.Finalized
			l.FinalizedAt = now
			return nil
		})
	})
	return result, err
}

// ForkLine 基于一条已有测线（草稿或定稿）建立全新草稿。引用版本原样复制，
// 不会自动升级到新版本；用户随后自行替换需要更新的位置引用。
func (s *Service) ForkLine(ctx context.Context, sourceID string, input ForkLine) (line.Line, error) {
	name := strings.TrimSpace(input.Name)
	note := strings.TrimSpace(input.Note)
	if note != "" {
		if err := geology.Text("note", note, 0, line.MaxNoteLength); err != nil {
			return line.Line{}, err
		}
	}
	id, err := newLineID()
	if err != nil {
		return line.Line{}, err
	}
	var result line.Line
	err = s.repo.Update(ctx, func(state *persistence.State) (bool, error) {
		if len(state.Lines) >= maxLines {
			return false, geology.Conflict("最多保存 1000 条测线")
		}
		source, ok := state.Lines[sourceID]
		if !ok {
			return false, geology.Missing("源测线不存在")
		}
		if name == "" {
			name = source.Name
		}
		if err := geology.Text("name", name, 1, line.MaxNameLength); err != nil {
			return false, err
		}
		for _, st := range source.Stations {
			revision, err := state.Revision(st.ProfileID, st.Version)
			if err != nil {
				return false, geology.Invalid("stations", "源测线引用的剖面版本缺失")
			}
			if revision.Profile.State != geology.Sealed {
				return false, geology.Conflict("源测线引用了未锁定版本")
			}
		}
		now := time.Now().UTC()
		result = line.Line{
			ID:        id,
			Name:      name,
			Note:      note,
			State:     line.Draft,
			Stations:  append([]line.Station{}, source.Stations...),
			Revision:  1,
			SourceID:  source.ID,
			CreatedAt: now,
			UpdatedAt: now,
		}
		if err := result.Validate(); err != nil {
			return false, err
		}
		state.Lines[id] = result.Clone()
		return true, nil
	})
	return result, err
}

// mutateLine 在写事务内加载、修改、校验并保存一条测线。
// mutate 可在确定新 UpdatedAt 后回填定稿时间，保证时间顺序一致。
func (s *Service) mutateLine(state *persistence.State, id string, out *line.Line, mutate func(l *line.Line, now time.Time) error) (bool, error) {
	current, ok := state.Lines[id]
	if !ok {
		return false, geology.Missing("测线不存在")
	}
	working := current.Clone()
	now := nextTime(current.UpdatedAt)
	if err := mutate(&working, now); err != nil {
		return false, err
	}
	working.Revision = current.Revision + 1
	working.UpdatedAt = now
	if err := working.Validate(); err != nil {
		return false, err
	}
	state.Lines[id] = working
	*out = working.Clone()
	return true, nil
}

// statusesFor 汇总测线各引用剖面的当前情况；outdated 仅作提示，不改变冻结引用。
func statusesFor(state persistence.State, l line.Line) map[string]line.ProfileStatus {
	statuses := make(map[string]line.ProfileStatus, len(l.Stations))
	for _, st := range l.Stations {
		revision, err := state.Revision(st.ProfileID, st.Version)
		if err != nil {
			continue
		}
		latest, err := state.Latest(st.ProfileID)
		if err != nil {
			continue
		}
		statuses[st.ProfileID] = line.ProfileStatus{
			Name:           revision.Profile.Name,
			CurrentVersion: latest.Version,
			CurrentState:   latest.State,
		}
	}
	return statuses
}

func (s *Service) Line(ctx context.Context, id string) (line.LineView, error) {
	var view line.LineView
	err := s.repo.View(ctx, func(state persistence.State) error {
		l, ok := state.Lines[id]
		if !ok {
			return geology.Missing("测线不存在")
		}
		view = line.View(l, statusesFor(state, l))
		return nil
	})
	return view, err
}

// Adjacency 表达一个位置在规范顺序中的稳定相邻关系。prev/current/next 都来自
// Sorted 后的同一顺序，因此并列位置也有确定的上一位置/下一位置，与存储顺序无关。
type Adjacency struct {
	LineID  string            `json:"line_id"`
	State   line.State        `json:"state"`
	Prev    *line.StationView `json:"prev"`
	Current line.StationView  `json:"current"`
	Next    *line.StationView `json:"next"`
}

func (s *Service) Adjacent(ctx context.Context, lineID, profileID string) (Adjacency, error) {
	if !geology.ValidID(profileID, "prf_") {
		return Adjacency{}, geology.Invalid("profile_id", "剖面编号无效")
	}
	var result Adjacency
	err := s.repo.View(ctx, func(state persistence.State) error {
		l, ok := state.Lines[lineID]
		if !ok {
			return geology.Missing("测线不存在")
		}
		view := line.View(l, statusesFor(state, l))
		index := -1
		for i := range view.Stations {
			if view.Stations[i].ProfileID == profileID {
				index = i
				break
			}
		}
		if index < 0 {
			return geology.Missing("该剖面不在这条测线上")
		}
		result = Adjacency{LineID: l.ID, State: l.State, Current: view.Stations[index]}
		if index > 0 {
			prev := view.Stations[index-1]
			result.Prev = &prev
		}
		if index+1 < len(view.Stations) {
			next := view.Stations[index+1]
			result.Next = &next
		}
		return nil
	})
	return result, err
}

type LinePage struct {
	Items  []line.LineView `json:"items"`
	Total  int             `json:"total"`
	Offset int             `json:"offset"`
	Limit  int             `json:"limit"`
}

func (s *Service) Lines(ctx context.Context, nameQuery string, filterState line.State, offset, limit int) (LinePage, error) {
	if offset < 0 || offset > 1000000 || limit < 1 || limit > 100 {
		return LinePage{}, geology.Invalid("pagination", "分页参数超出范围")
	}
	if err := geology.Text("q", nameQuery, 0, line.MaxNameLength); err != nil {
		return LinePage{}, err
	}
	if filterState != "" && filterState != line.Draft && filterState != line.Finalized {
		return LinePage{}, geology.Invalid("state", "不支持的测线状态")
	}
	page := LinePage{Items: []line.LineView{}, Offset: offset, Limit: limit}
	err := s.repo.View(ctx, func(state persistence.State) error {
		all := make([]line.Line, 0, len(state.Lines))
		for _, l := range state.Lines {
			if filterState != "" && l.State != filterState {
				continue
			}
			if nameQuery != "" && !strings.Contains(strings.ToLower(l.Name), strings.ToLower(nameQuery)) {
				continue
			}
			all = append(all, l)
		}
		sort.Slice(all, func(i, j int) bool {
			if !all[i].UpdatedAt.Equal(all[j].UpdatedAt) {
				return all[i].UpdatedAt.After(all[j].UpdatedAt)
			}
			return all[i].ID < all[j].ID
		})
		page.Total = len(all)
		start := min(offset, len(all))
		end := min(start+limit, len(all))
		for i := start; i < end; i++ {
			page.Items = append(page.Items, line.View(all[i], statusesFor(state, all[i])))
		}
		return nil
	})
	return page, err
}
