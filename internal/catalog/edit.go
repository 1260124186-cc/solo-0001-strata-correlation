package catalog

import (
	"context"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/geology"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/persistence"
)

type EditMetadata struct {
	ExpectedVersion int              `json:"expected_version"`
	Metadata        geology.Metadata `json:"metadata"`
	Reason          string           `json:"reason"`
}

type ReplaceLayers struct {
	ExpectedVersion int             `json:"expected_version"`
	Layers          []geology.Layer `json:"layers"`
	Reason          string          `json:"reason"`
}

type StateChange struct {
	ExpectedVersion int    `json:"expected_version"`
	Reason          string `json:"reason"`
	// Rules 为可选的一次性锁定门槛覆盖。省略时使用服务启动配置中的默认规则集合。
	// 一旦锁定成功，使用的规则集合与结论随版本冻结。
	Rules *geology.RuleSet `json:"rules,omitempty"`
}

func (s *Service) Edit(ctx context.Context, id string, input EditMetadata) (geology.Profile, error) {
	metadata, err := geology.NormalizeMetadata(input.Metadata)
	if err != nil {
		return geology.Profile{}, err
	}
	reason, err := normalizedReason(input.Reason)
	if err != nil {
		return geology.Profile{}, err
	}
	var result geology.Profile
	err = s.repo.Update(ctx, func(state *persistence.State) (bool, error) {
		p, err := state.Latest(id)
		if err != nil {
			return false, err
		}
		if err = geology.CheckEditable(p, input.ExpectedVersion); err != nil {
			return false, err
		}
		p.Metadata = metadata
		p.Version++
		p.UpdatedAt = nextTime(p.UpdatedAt)
		revision := geology.Revision{Profile: p, Event: geology.Event{Action: "metadata", Reason: reason, Version: p.Version, At: p.UpdatedAt}}
		if err = appendRevision(state, revision); err != nil {
			return false, err
		}
		result = p
		return true, nil
	})
	return result, err
}

func (s *Service) Replace(ctx context.Context, id string, input ReplaceLayers) (geology.Profile, error) {
	if input.Layers == nil {
		return geology.Profile{}, geology.Invalid("layers", "必须提供分层数组，清空时使用 []")
	}
	reason, err := normalizedReason(input.Reason)
	if err != nil {
		return geology.Profile{}, err
	}
	var result geology.Profile
	err = s.repo.Update(ctx, func(state *persistence.State) (bool, error) {
		p, err := state.Latest(id)
		if err != nil {
			return false, err
		}
		if err = geology.CheckEditable(p, input.ExpectedVersion); err != nil {
			return false, err
		}
		layers, err := geology.NormalizeLayers(input.Layers, p.DepthMM)
		if err != nil {
			return false, err
		}
		p.Layers = layers
		p.Version++
		p.UpdatedAt = nextTime(p.UpdatedAt)
		revision := geology.Revision{Profile: p, Event: geology.Event{Action: "layers", Reason: reason, Version: p.Version, At: p.UpdatedAt}}
		if err = appendRevision(state, revision); err != nil {
			return false, err
		}
		result = p
		return true, nil
	})
	return result, err
}

func (s *Service) Change(ctx context.Context, id string, target geology.State, input StateChange) (geology.Profile, error) {
	reason, err := normalizedReason(input.Reason)
	if err != nil {
		return geology.Profile{}, err
	}
	if target != geology.Sealed && input.Rules != nil {
		return geology.Profile{}, geology.Invalid("rules", "只有锁定请求才能指定完整性规则集合")
	}
	// 在进入事务前确定本次门槛并规范化；规则配置本身错误返回 422，
	// 规则命中分层问题在状态机内部以 409 返回。
	rules := s.defaultRules
	if input.Rules != nil {
		rules, err = geology.NormalizeRuleSet(*input.Rules)
		if err != nil {
			return geology.Profile{}, err
		}
	}
	var result geology.Profile
	err = s.repo.Update(ctx, func(state *persistence.State) (bool, error) {
		p, err := state.Latest(id)
		if err != nil {
			return false, err
		}
		revision, err := geology.ChangeState(p, target, input.ExpectedVersion, reason, rules, nextTime(p.UpdatedAt))
		if err != nil {
			return false, err
		}
		if err = appendRevision(state, revision); err != nil {
			return false, err
		}
		result = revision.Profile
		return true, nil
	})
	return result, err
}
