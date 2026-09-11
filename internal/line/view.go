package line

import (
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/geology"
	"time"
)

// StationView 是一个位置在测线顺序中的稳定表达，供相邻查询与展示使用。
// 同一距离出现并列时，TieGroupIndex / TieGroupSize 给出与内部存储无关的稳定先后。
type StationView struct {
	Station
	Order          int           `json:"order"`           // 1 基全局顺序，沿距离升序
	TieGroupIndex  int           `json:"tie_group_index"` // 1 基，在同距离并列组内的先后
	TieGroupSize   int           `json:"tie_group_size"`  // 该距离并列组内的位置总数
	Tied           bool          `json:"tied"`            // 是否与其他位置距离相同（存在并列）
	ProfileName    string        `json:"profile_name"`    // 绑定版本当时的剖面名称快照
	CurrentVersion int           `json:"current_version"` // 该剖面当前最新版本号
	CurrentState   geology.State `json:"current_state"`   // 该剖面当前状态
	Outdated       bool          `json:"outdated"`        // 绑定版本之后是否已有更新（仅提示，不自动替换）
}

// LineView 是测线及其规范化顺序的完整视图。定稿与草稿返回相同形状，便于历史查询。
type LineView struct {
	ID          string        `json:"id"`
	Name        string        `json:"name"`
	Note        string        `json:"note"`
	State       State         `json:"state"`
	Revision    int           `json:"revision"`
	SourceID    string        `json:"source_id,omitempty"`
	Stations    []StationView `json:"stations"`
	CreatedAt   time.Time     `json:"created_at"`
	UpdatedAt   time.Time     `json:"updated_at"`
	FinalizedAt *time.Time    `json:"finalized_at,omitempty"`
}

// ProfileStatus 由 catalog 提供，描述一个被引用剖面的当前情况，用于生成更新提示。
type ProfileStatus struct {
	Name           string
	CurrentVersion int
	CurrentState   geology.State
}

// View 在给定规范顺序与剖面现状的前提下构造稳定视图。
// current 以 profile_id 为键；缺失键表示引用的剖面在当前状态中找不到（数据异常）。
// 该函数不改变入参，也不依赖 map 遍历顺序——站点顺序完全来自 Sorted。
func View(l Line, current map[string]ProfileStatus) LineView {
	ordered := Sorted(l.Stations)
	// groupStart[i] 记录第 i 个站点所属并列组的首个 0 基下标。
	groupStart := make([]int, len(ordered))
	for i := 0; i < len(ordered); {
		j := i + 1
		for j < len(ordered) && ordered[j].ChainageMM == ordered[i].ChainageMM {
			j++
		}
		for k := i; k < j; k++ {
			groupStart[k] = i
		}
		i = j
	}
	views := make([]StationView, 0, len(ordered))
	for i, st := range ordered {
		start := groupStart[i]
		size := 0
		for start+size < len(ordered) && ordered[start+size].ChainageMM == st.ChainageMM {
			size++
		}
		sv := StationView{
			Station:       st,
			Order:         i + 1,
			TieGroupIndex: i - start + 1,
			TieGroupSize:  size,
			Tied:          size > 1,
		}
		if status, ok := current[st.ProfileID]; ok {
			sv.ProfileName = status.Name
			sv.CurrentVersion = status.CurrentVersion
			sv.CurrentState = status.CurrentState
			sv.Outdated = status.CurrentVersion != st.Version
		}
		views = append(views, sv)
	}
	out := LineView{
		ID:        l.ID,
		Name:      l.Name,
		Note:      l.Note,
		State:     l.State,
		Revision:  l.Revision,
		SourceID:  l.SourceID,
		Stations:  views,
		CreatedAt: l.CreatedAt,
		UpdatedAt: l.UpdatedAt,
	}
	if !l.FinalizedAt.IsZero() {
		at := l.FinalizedAt
		out.FinalizedAt = &at
	}
	return out
}
