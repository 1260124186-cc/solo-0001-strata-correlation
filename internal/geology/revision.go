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

// Adopt 以草拟剖面为基础，整体采用某个历史版本的元数据和分层，
// 产生一个新的草拟版本；历史版本本身不回退也不被删除。
func Adopt(p Profile, source Profile, expected, sourceVersion int, reason string, now time.Time) (Revision, error) {
	if p.Version != expected {
		return Revision{}, VersionConflict(expected, p.Version)
	}
	if err := Text("reason", reason, 1, 500); err != nil {
		return Revision{}, err
	}
	if p.State != Draft {
		return Revision{}, Conflict("剖面已锁定，请先重新打开")
	}
	if source.ID != p.ID {
		return Revision{}, Invalid("source_version", "来源版本必须属于同一剖面")
	}
	if source.Version != sourceVersion || sourceVersion < 1 || sourceVersion >= p.Version {
		return Revision{}, Invalid("source_version", "来源版本必须是当前版本之前的历史版本")
	}
	p = p.Clone()
	p.Metadata = source.Metadata
	p.Layers = append([]Layer{}, source.Layers...)
	p.Version++
	p.UpdatedAt = now
	p.State = Draft
	return Revision{p, Event{Action: "adopt", Reason: reason, Version: p.Version, SourceVersion: sourceVersion, At: now}}, nil
}
