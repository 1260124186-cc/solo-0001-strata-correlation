package line

import (
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/geology"
	"sort"
	"time"
)

// State 是测线的生命周期状态。定稿后内容冻结，不能再修改或重新打开。
type State string

const (
	Draft     State = "draft"
	Finalized State = "finalized"

	// MaxStations 限制单条测线的站点数量，与单剖面层数上限保持同一量级。
	MaxStations = 500
	// MaxChainageMM 将手工填写的沿线距离限制在 100 千米以内（整数毫米）。
	MaxChainageMM int64 = 100_000_000
	// MaxNameLength / MaxNoteLength 与剖面文字限制保持一致风格。
	MaxNameLength = 120
	MaxNoteLength = 2000
)

// Station 是测线上的一个位置。距离由编录人员沿现场测线手工填写；
// 引用绑定到一个具体的、已锁定的剖面历史版本。
type Station struct {
	ProfileID  string `json:"profile_id"`
	Version    int    `json:"version"`
	ChainageMM int64  `json:"chainage_mm"`
	// TieRank 仅在距离相同的并列组内区分先后，1 为该并列组最靠前的位置。
	TieRank int `json:"tie_rank"`
}

// Line 是一份按现场测线排列的剖面顺序。草稿可反复调整；定稿是不可变历史记录。
type Line struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Note        string    `json:"note"`
	State       State     `json:"state"`
	Stations    []Station `json:"stations"`
	Revision    int       `json:"revision"`
	SourceID    string    `json:"source_id,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	FinalizedAt time.Time `json:"finalized_at,omitempty"`
}

func (l Line) Clone() Line {
	l.Stations = append([]Station{}, l.Stations...)
	return l
}

// less 定义与存储无关的全局确定顺序：先沿距离，再并列序号，最后以剖面编号兜底。
// profile_id 兜底保证任何情况下相邻查询结果都稳定，不依赖切片或 map 的物理顺序。
func less(a, b Station) bool {
	if a.ChainageMM != b.ChainageMM {
		return a.ChainageMM < b.ChainageMM
	}
	if a.TieRank != b.TieRank {
		return a.TieRank < b.TieRank
	}
	return a.ProfileID < b.ProfileID
}

// Sorted 返回按规范顺序排列的站点副本，调用方可安全用于相邻查询与展示。
func Sorted(stations []Station) []Station {
	out := append([]Station{}, stations...)
	sort.SliceStable(out, func(i, j int) bool { return less(out[i], out[j]) })
	return out
}

// canonicalize 校验站点形状、拒绝同位置重复、计算并列序号并按规范顺序返回。
// 这里只做测线自身可判定的规则；引用的剖面版本是否存在、是否锁定由 catalog 校验。
func canonicalize(stations []Station) ([]Station, error) {
	if len(stations) > MaxStations {
		return nil, geology.Invalid("stations", "单条测线最多 500 个位置")
	}
	seen := make(map[string]bool, len(stations))
	out := append([]Station{}, stations...)
	for i, st := range out {
		field := "stations"
		if !geology.ValidID(st.ProfileID, "prf_") {
			return nil, geology.Invalid(field, "位置必须引用有效的剖面编号")
		}
		if st.Version < 1 {
			return nil, geology.Invalid(field, "位置必须绑定正整数的锁定剖面版本")
		}
		if st.ChainageMM < 0 || st.ChainageMM > MaxChainageMM {
			return nil, geology.Invalid(field, "沿线距离必须介于 0 和 100000000 毫米之间")
		}
		// 同一位置重复：一条测线中同一剖面只能出现一次，距离相同也不能消除歧义。
		if seen[st.ProfileID] {
			return nil, geology.Conflict("同一剖面在一条测线中不能重复出现")
		}
		seen[st.ProfileID] = true
		out[i].TieRank = 0 // 先忽略调用方提供的并列序号，统一重新计算。
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].ChainageMM != out[j].ChainageMM {
			return out[i].ChainageMM < out[j].ChainageMM
		}
		return out[i].ProfileID < out[j].ProfileID
	})
	for i := range out {
		rank := 1
		if i > 0 && out[i].ChainageMM == out[i-1].ChainageMM {
			rank = out[i-1].TieRank + 1
		}
		out[i].TieRank = rank
	}
	return out, nil
}

// Canonicalize 暴露给 catalog 使用：新增或整体替换站点时按 profile_id 决定并列先后。
func Canonicalize(stations []Station) ([]Station, error) {
	return canonicalize(stations)
}

// Reorder 按用户给出的完整位置顺序重新分配并列序号。
// order 必须恰好包含现有全部 profile_id 且不重不漏；同时要求沿线距离单调不减，
// 即用户不能借“调整排序”制造物理上倒退的测线，只能消解同一距离的并列先后。
func Reorder(stations []Station, order []string) ([]Station, error) {
	if len(order) != len(stations) {
		return nil, geology.Invalid("order", "排序必须包含测线中的全部位置，且数量一致")
	}
	byID := make(map[string]Station, len(stations))
	for _, st := range stations {
		byID[st.ProfileID] = st
	}
	arranged := make([]Station, 0, len(order))
	seen := make(map[string]bool, len(order))
	for _, id := range order {
		st, ok := byID[id]
		if !ok {
			return nil, geology.Invalid("order", "排序中出现了不属于该测线的剖面")
		}
		if seen[id] {
			return nil, geology.Invalid("order", "排序中的剖面不能重复")
		}
		seen[id] = true
		arranged = append(arranged, st)
	}
	for i := 1; i < len(arranged); i++ {
		if arranged[i].ChainageMM < arranged[i-1].ChainageMM {
			return nil, geology.Conflict("调整排序后沿线距离必须单调不减，需要改变相对位置请修改距离")
		}
	}
	for i := range arranged {
		rank := 1
		if i > 0 && arranged[i].ChainageMM == arranged[i-1].ChainageMM {
			rank = arranged[i-1].TieRank + 1
		}
		arranged[i].TieRank = rank
	}
	return Sorted(arranged), nil
}

// Validate 供持久化恢复时检查一份测线的内部不变量。
func (l Line) Validate() error {
	if !geology.ValidID(l.ID, "line_") {
		return geology.Invalid("id", "测线编号无效")
	}
	if err := geology.Text("name", l.Name, 1, MaxNameLength); err != nil {
		return err
	}
	if err := geology.Text("note", l.Note, 0, MaxNoteLength); err != nil {
		return err
	}
	if l.State != Draft && l.State != Finalized {
		return geology.Invalid("state", "测线状态未知")
	}
	if l.Revision < 1 {
		return geology.Invalid("revision", "测线修订号必须为正整数")
	}
	if l.CreatedAt.IsZero() || l.UpdatedAt.Before(l.CreatedAt) {
		return geology.Invalid("time", "时间顺序无效")
	}
	if l.State == Finalized && l.FinalizedAt.IsZero() {
		return geology.Invalid("finalized_at", "定稿测线缺少定稿时间")
	}
	if l.State == Draft && !l.FinalizedAt.IsZero() {
		return geology.Invalid("finalized_at", "草稿不应带定稿时间")
	}
	if len(l.Stations) > MaxStations {
		return geology.Invalid("stations", "站点数量超过上限")
	}
	ids := make(map[string]bool, len(l.Stations))
	for i, st := range l.Stations {
		if !geology.ValidID(st.ProfileID, "prf_") || st.Version < 1 {
			return geology.Invalid("stations", "站点引用无效")
		}
		if st.ChainageMM < 0 || st.ChainageMM > MaxChainageMM {
			return geology.Invalid("stations", "站点距离超出范围")
		}
		if ids[st.ProfileID] {
			return geology.Invalid("stations", "同一剖面在测线中重复")
		}
		ids[st.ProfileID] = true
		if i > 0 {
			prev := l.Stations[i-1]
			if prev.ChainageMM > st.ChainageMM {
				return geology.Invalid("stations", "站点距离必须单调不减")
			}
			if prev.ChainageMM == st.ChainageMM {
				if st.TieRank != prev.TieRank+1 {
					return geology.Invalid("stations", "并列位置序号必须连续")
				}
			} else if st.TieRank != 1 {
				return geology.Invalid("stations", "每个距离段的并列序号必须从 1 开始")
			}
		} else if st.TieRank != 1 {
			return geology.Invalid("stations", "首个位置并列序号必须为 1")
		}
	}
	return nil
}
