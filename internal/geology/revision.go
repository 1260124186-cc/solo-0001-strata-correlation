package geology

import "time"

// MainBranch 是每个剖面的主修订线标识。其余修订线使用 br_ 前缀编号。
const MainBranch = "main"

// MergeRef 记录一次合并的来源线与三向合并基点。
type MergeRef struct {
	Source        string `json:"source"`         // 被合并的分叉线编号
	SourceVersion int    `json:"source_version"` // 被合并的来源线头版本
	BaseVersion   int    `json:"base_version"`   // 主线与来源线的共同基点版本
}

// Event 描述一个版本的来源。线性历史中 Branch 恒为 main、Parent 为上一版本；
// 分叉固定 ForkPoint，合并通过 Merge 记录来源线和基点。
type Event struct {
	Action    string    `json:"action"`
	Reason    string    `json:"reason"`
	Version   int       `json:"version"`
	At        time.Time `json:"at"`
	Branch    string    `json:"branch,omitempty"`
	Parent    int       `json:"parent,omitempty"`
	ForkPoint int       `json:"fork_point,omitempty"`
	Name      string    `json:"name,omitempty"`
	Merge     *MergeRef `json:"merge,omitempty"`
}

type Revision struct {
	Profile Profile `json:"profile"`
	Event   Event   `json:"event"`
}

func (r Revision) Clone() Revision {
	r.Profile = r.Profile.Clone()
	if r.Event.Merge != nil {
		merge := *r.Event.Merge
		r.Event.Merge = &merge
	}
	return r
}

// ValidBranch 校验修订线标识：主线或 br_ 前缀加 32 位十六进制。
// 用户为分叉线提供的名称不属于标识，另由 NormalizeBranchName 校验。
func ValidBranch(branch string) bool {
	if branch == MainBranch {
		return true
	}
	return ValidID(branch, "br_")
}

func CheckEditable(p Profile, expected int) error {
	if p.Version != expected {
		return VersionConflict(expected, p.Version)
	}
	if p.State != Draft {
		return Conflict("该修订线已锁定，请先重新打开")
	}
	return nil
}

// ChangeState 在给定头版本上产生状态变更，新版本号由调用方按真实历史分配。
func ChangeState(p Profile, target State, expected, newVersion int, reason string, now time.Time) (Revision, error) {
	if p.Version != expected {
		return Revision{}, VersionConflict(expected, p.Version)
	}
	if err := Text("reason", reason, 1, 500); err != nil {
		return Revision{}, err
	}
	if newVersion <= p.Version {
		return Revision{}, Invalid("version", "新版本号必须大于当前版本")
	}
	if p.State == target {
		return Revision{}, Conflict("该修订线已经处于目标状态")
	}
	action := "reopen"
	if target == Sealed {
		if !CoverageOf(p).Ready {
			return Revision{}, Conflict("分层存在深度缺口，不能锁定")
		}
		action = "seal"
	} else if target != Draft {
		return Revision{}, Invalid("state", "不支持的状态")
	}
	p = p.Clone()
	p.Version = newVersion
	p.UpdatedAt = now
	p.State = target
	return Revision{p, Event{Action: action, Reason: reason, Version: newVersion, At: now, Branch: p.Branch}}, nil
}
