package catalog

import (
	"context"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/geology"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/persistence"
)

// IntegrityExplanation 回答"某个历史版本当时/现在是否满足锁定门槛"。
//
// 锁定版本返回 basis="sealed"：规则集合与结论是锁定时冻结在该版本事件里的，
// 后续修改分层或调整服务配置都不会改变它。
// 未锁定版本（草稿、创建态）返回 basis="live"：使用该版本自身的分层，
// 按服务当前默认规则集合现场解释；它不是任何历史结论，仅用于锁定前预检。
type IntegrityExplanation struct {
	Version int                     `json:"version"`
	State   geology.State           `json:"state"`
	Basis   string                  `json:"basis"`
	Rules   []string                `json:"rules"`
	Report  geology.IntegrityReport `json:"report"`
}

func (s *Service) Integrity(ctx context.Context, id string, version int) (IntegrityExplanation, error) {
	var result IntegrityExplanation
	err := s.repo.View(ctx, func(state persistence.State) error {
		if version == 0 {
			latest, err := state.Latest(id)
			if err != nil {
				return err
			}
			version = latest.Version
		}
		revision, err := state.Revision(id, version)
		if err != nil {
			return err
		}
		result.Version = revision.Profile.Version
		result.State = revision.Profile.State
		if revision.Event.Action == "seal" && revision.Event.Integrity != nil {
			// 历史结论：直接读冻结记录，绝不使用当前分层重新计算。
			result.Basis = "sealed"
			result.Report = *revision.Event.Integrity
			result.Rules = revision.Event.Integrity.Rules
			return nil
		}
		// 非锁定版本没有冻结结论；用"该版本自己的分层"+当前默认规则现场解释。
		result.Basis = "live"
		result.Rules = s.defaultRules.Rules
		result.Report = geology.EvaluateIntegrity(revision.Profile, s.defaultRules)
		return nil
	})
	return result, err
}

// CoverageView 在同一读快照中返回当前覆盖统计和默认门槛的现场解释，
// 使 coverage 接口的"能否锁定"与实际锁定判定一致。
type CoverageView struct {
	Version   int                  `json:"version"`
	Coverage  geology.Coverage     `json:"coverage"`
	Integrity IntegrityExplanation `json:"integrity"`
}

func (s *Service) Coverage(ctx context.Context, id string) (CoverageView, error) {
	var view CoverageView
	err := s.repo.View(ctx, func(state persistence.State) error {
		p, err := state.Latest(id)
		if err != nil {
			return err
		}
		view.Version = p.Version
		view.Coverage = geology.CoverageOf(p)
		view.Integrity = IntegrityExplanation{
			Version: p.Version,
			State:   p.State,
			Basis:   "live",
			Rules:   s.defaultRules.Rules,
			Report:  geology.EvaluateIntegrity(p, s.defaultRules),
		}
		return nil
	})
	return view, err
}
