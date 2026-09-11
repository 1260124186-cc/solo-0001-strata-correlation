package geology

import "time"

type Event struct {
	Action  string    `json:"action"`
	Reason  string    `json:"reason"`
	Version int       `json:"version"`
	At      time.Time `json:"at"`
	// Integrity 仅锁定事件存在，记录当时使用的规则集合及其确定性解释。
	// 它与分层快照一起不可变地保存，历史解释永远以这份记录为准。
	Integrity *IntegrityReport `json:"integrity,omitempty"`
}

type Revision struct {
	Profile Profile `json:"profile"`
	Event   Event   `json:"event"`
}

func (r Revision) Clone() Revision {
	r.Profile = r.Profile.Clone()
	if r.Event.Integrity != nil {
		report := *r.Event.Integrity
		report.Rules = append([]string{}, r.Event.Integrity.Rules...)
		report.Findings = append([]Finding{}, r.Event.Integrity.Findings...)
		for i := range report.Findings {
			report.Findings[i].Intervals = append([]Interval{}, report.Findings[i].Intervals...)
		}
		if r.Event.Integrity.MarkerGroups != nil {
			report.MarkerGroups = make([][]string, len(r.Event.Integrity.MarkerGroups))
			for i, group := range r.Event.Integrity.MarkerGroups {
				report.MarkerGroups[i] = append([]string{}, group...)
			}
		}
		if r.Event.Integrity.MinLayerMM != nil {
			minimum := *r.Event.Integrity.MinLayerMM
			report.MinLayerMM = &minimum
		}
		r.Event.Integrity = &report
	}
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

// ChangeState 执行锁定或重新打开。锁定时 rules 必须是已规范化的规则集合；
// 规则命中即拒绝并返回问题深度区间，通过则把解释结论冻结进锁定事件。
func ChangeState(p Profile, target State, expected int, reason string, rules RuleSet, now time.Time) (Revision, error) {
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
	var report *IntegrityReport
	if target == Sealed {
		// 再规范化一次保证调用方传入的是确定性形态；规则配置错误属于 422。
		normalized, err := NormalizeRuleSet(rules)
		if err != nil {
			return Revision{}, err
		}
		rules = normalized
		evaluated := EvaluateIntegrity(p, rules)
		if !evaluated.Passed {
			return Revision{}, LockConflict(evaluated.Findings)
		}
		report = &evaluated
		action = "seal"
	} else if target != Draft {
		return Revision{}, Invalid("state", "不支持的状态")
	}
	p = p.Clone()
	p.Version++
	p.UpdatedAt = now
	p.State = target
	return Revision{p, Event{Action: action, Reason: reason, Version: p.Version, At: now, Integrity: report}}, nil
}
