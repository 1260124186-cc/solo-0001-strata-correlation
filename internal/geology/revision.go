package geology

import "time"

type Event struct {
	Action        string    `json:"action"`
	Reason        string    `json:"reason"`
	Version       int       `json:"version"`
	SourceVersion int       `json:"source_version,omitempty"`
	At            time.Time `json:"at"`
}

type Revision struct {
	Profile Profile `json:"profile"`
	Event   Event   `json:"event"`
}

func (r Revision) Clone() Revision {
	r.Profile = r.Profile.Clone()
	return r
}

func CheckEditable(p Profile, expected int) error {
	if p.Version != expected {
		return VersionConflict(expected, p.Version)
	}
	if p.State != Draft {
		return Conflict("剖面已锁定，请先重新打开")
	}
	return nil
}

func ChangeState(p Profile, target State, expected int, reason string, now time.Time) (Revision, error) {
	if p.Version != expected {
		return Revision{}, VersionConflict(expected, p.Version)
	}
	if err := Text("reason", reason, 1, 500); err != nil {
		return Revision{}, err
	}
	if p.State == target {
		return Revision{}, Conflict("剖面已经处于目标状态")
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
	p.Version++
	p.UpdatedAt = now
	p.State = target
	return Revision{p, Event{Action: action, Reason: reason, Version: p.Version, At: now}}, nil
}

// Adopt 在保持历史不变的前提下，将同一剖面某个历史版本的元数据和分层
// 复制到当前草拟版本之后，产生新的草拟版本。
func Adopt(p Profile, source Revision, expected int, reason string, now time.Time) (Revision, error) {
	if p.Version != expected {
		return Revision{}, VersionConflict(expected, p.Version)
	}
	if err := Text("reason", reason, 1, 500); err != nil {
		return Revision{}, err
	}
	if p.State != Draft {
		return Revision{}, Conflict("剖面已锁定，请先重新打开")
	}
	if source.Profile.ID != p.ID {
		return Revision{}, Invalid("source_version", "来源版本必须属于同一剖面")
	}
	if source.Profile.Version < 1 || source.Profile.Version > p.Version {
		return Revision{}, Invalid("source_version", "来源版本不存在")
	}
	if source.Profile.Version == p.Version {
		return Revision{}, Conflict("当前版本已经是来源版本，无需采用")
	}
	next := p.Clone()
	next.Metadata = source.Profile.Metadata
	next.Layers = append([]Layer{}, source.Profile.Layers...)
	next.State = Draft
	next.Version = p.Version + 1
	next.UpdatedAt = now
	return Revision{next, Event{Action: "adopt", Reason: reason, Version: next.Version, SourceVersion: source.Profile.Version, At: now}}, nil
}
