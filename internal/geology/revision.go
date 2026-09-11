package geology

import "time"

type Event struct {
	Action  string    `json:"action"`
	Reason  string    `json:"reason"`
	Version int       `json:"version"`
	At      time.Time `json:"at"`
}

// RevisionRef pins one historical revision of a profile.
type RevisionRef struct {
	ID      string `json:"id"`
	Version int    `json:"version"`
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
