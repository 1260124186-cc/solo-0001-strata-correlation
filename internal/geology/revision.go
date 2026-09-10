package geology

import "time"

type Event struct {
	Action  string    `json:"action"`
	Reason  string    `json:"reason"`
	Version int       `json:"version"`
	At      time.Time `json:"at"`
}

type Revision struct {
	Profile Profile `json:"profile"`
	Event   Event   `json:"event"`
}

func (r Revision) Clone() Revision {
	r.Profile = r.Profile.Clone()
	return r
}

// RequireDraft expresses the rule shared by metadata and layer revisions:
// only an open (draft) profile may be edited. Version checking is handled by
// the caller so every revision path enforces it the same way.
func RequireDraft(p Profile) error {
	if p.State != Draft {
		return Conflict("剖面已锁定，请先重新打开")
	}
	return nil
}

// StateTransitionAction expresses the rules of a lock/reopen request and
// returns the history action recorded for it. It only inspects state; the
// version check, timestamp and event assembly belong to the revision pipeline.
func StateTransitionAction(p Profile, target State) (string, error) {
	if p.State == target {
		return "", Conflict("剖面已经处于目标状态")
	}
	if target == Sealed {
		if !CoverageOf(p).Ready {
			return "", Conflict("分层存在深度缺口，不能锁定")
		}
		return "seal", nil
	}
	if target != Draft {
		return "", Invalid("state", "不支持的状态")
	}
	return "reopen", nil
}
