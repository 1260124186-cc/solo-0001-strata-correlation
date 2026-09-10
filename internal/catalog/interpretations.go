package catalog

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/correlation"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/geology"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/persistence"
	"sort"
	"time"
)

type CreateInterpretation struct {
	Left       correlation.Reference      `json:"left"`
	Right      correlation.Reference      `json:"right"`
	Pairs      []correlation.IntervalPair `json:"pairs"`
	Rationale  string                     `json:"rationale"`
	Confidence correlation.Confidence     `json:"confidence"`
}

type ReviseInterpretation struct {
	ExpectedVersion int                        `json:"expected_version"`
	Pairs           []correlation.IntervalPair `json:"pairs"`
	Rationale       string                     `json:"rationale"`
	Confidence      correlation.Confidence     `json:"confidence"`
	Reason          string                     `json:"reason"`
}

type InterpretationPage struct {
	Items  []correlation.Interpretation `json:"items"`
	Total  int                          `json:"total"`
	Offset int                          `json:"offset"`
	Limit  int                          `json:"limit"`
}

func newInterpretationID() (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return "int_" + hex.EncodeToString(bytes), nil
}

// checkPairDepths resolves the referenced locked versions and verifies that
// every interval belongs to its own referenced version.
func checkPairDepths(state *persistence.State, left, right correlation.Reference, pairs []correlation.IntervalPair) error {
	leftRevision, err := state.Revision(left.ID, left.Version)
	if err != nil {
		return err
	}
	rightRevision, err := state.Revision(right.ID, right.Version)
	if err != nil {
		return err
	}
	if leftRevision.Profile.State != geology.Sealed || rightRevision.Profile.State != geology.Sealed {
		return geology.Conflict("对应解释需要绑定已经锁定的历史版本")
	}
	return correlation.ValidatePairDepths(pairs, leftRevision.Profile.DepthMM, rightRevision.Profile.DepthMM)
}

func appendInterpretation(state *persistence.State, revision correlation.InterpretationRevision) error {
	history := state.Interpretations[revision.Interpretation.ID]
	if len(history) >= correlation.MaxInterpretationVersions {
		return geology.Conflict("单份对应解释最多保留 500 个版本")
	}
	if revision.Interpretation.Version != len(history)+1 {
		return geology.Conflict("版本顺序不一致")
	}
	if err := revision.Interpretation.Validate(); err != nil {
		return err
	}
	state.Interpretations[revision.Interpretation.ID] = append(history, revision.Clone())
	return nil
}

func (s *Service) CreateInterpretation(ctx context.Context, input CreateInterpretation) (correlation.Interpretation, error) {
	if err := correlation.ValidateReferences(input.Left, input.Right); err != nil {
		return correlation.Interpretation{}, err
	}
	pairs, rationale, err := correlation.NormalizeContent(input.Pairs, input.Rationale, input.Confidence)
	if err != nil {
		return correlation.Interpretation{}, err
	}
	id, err := newInterpretationID()
	if err != nil {
		return correlation.Interpretation{}, err
	}
	now := time.Now().UTC()
	result := correlation.Interpretation{
		ID: id, Left: input.Left, Right: input.Right,
		Pairs: pairs, Rationale: rationale, Confidence: input.Confidence,
		State: correlation.DraftInterpretation, Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	err = s.repo.Update(ctx, func(state *persistence.State) (bool, error) {
		if len(state.Interpretations) >= correlation.MaxInterpretations {
			return false, geology.Conflict("最多保存 10000 份对应解释")
		}
		if _, exists := state.Interpretations[id]; exists {
			return false, geology.Conflict("对应解释编号重复，请重试")
		}
		if err := checkPairDepths(state, input.Left, input.Right, pairs); err != nil {
			return false, err
		}
		revision := correlation.InterpretationRevision{
			Interpretation: result,
			Event:          geology.Event{Action: "create", Reason: "新建对应解释", Version: 1, At: now},
		}
		if err := appendInterpretation(state, revision); err != nil {
			return false, err
		}
		return true, nil
	})
	return result, err
}

// ReviseInterpretation replaces the content of a draft interpretation, or
// starts a new draft version from a finalized one. Finalized versions
// themselves stay immutable.
func (s *Service) ReviseInterpretation(ctx context.Context, id string, input ReviseInterpretation) (correlation.Interpretation, error) {
	pairs, rationale, err := correlation.NormalizeContent(input.Pairs, input.Rationale, input.Confidence)
	if err != nil {
		return correlation.Interpretation{}, err
	}
	reason, err := normalizedReason(input.Reason)
	if err != nil {
		return correlation.Interpretation{}, err
	}
	var result correlation.Interpretation
	err = s.repo.Update(ctx, func(state *persistence.State) (bool, error) {
		history, exists := state.Interpretations[id]
		if !exists {
			return false, geology.Missing("对应解释不存在")
		}
		current := history[len(history)-1].Interpretation
		if current.Version != input.ExpectedVersion {
			return false, geology.VersionConflict(input.ExpectedVersion, current.Version)
		}
		if err := checkPairDepths(state, current.Left, current.Right, pairs); err != nil {
			return false, err
		}
		next := correlation.Interpretation{
			ID: id, Left: current.Left, Right: current.Right,
			Pairs: pairs, Rationale: rationale, Confidence: input.Confidence,
			State: correlation.DraftInterpretation, Version: current.Version + 1,
			CreatedAt: current.CreatedAt, UpdatedAt: nextTime(current.UpdatedAt),
		}
		action := "revise"
		if current.State == correlation.DraftInterpretation {
			action = "edit"
		}
		revision := correlation.InterpretationRevision{
			Interpretation: next,
			Event:          geology.Event{Action: action, Reason: reason, Version: next.Version, At: next.UpdatedAt},
		}
		if err := appendInterpretation(state, revision); err != nil {
			return false, err
		}
		result = next
		return true, nil
	})
	return result, err
}

// FinalizeInterpretation seals a draft. A finalized interpretation cannot be
// modified; further work goes through ReviseInterpretation on the final version.
func (s *Service) FinalizeInterpretation(ctx context.Context, id string, input StateChange) (correlation.Interpretation, error) {
	reason, err := normalizedReason(input.Reason)
	if err != nil {
		return correlation.Interpretation{}, err
	}
	var result correlation.Interpretation
	err = s.repo.Update(ctx, func(state *persistence.State) (bool, error) {
		history, exists := state.Interpretations[id]
		if !exists {
			return false, geology.Missing("对应解释不存在")
		}
		current := history[len(history)-1].Interpretation
		if current.Version != input.ExpectedVersion {
			return false, geology.VersionConflict(input.ExpectedVersion, current.Version)
		}
		if current.State != correlation.DraftInterpretation {
			return false, geology.Conflict("对应解释已经定稿")
		}
		next := current.Clone()
		next.State = correlation.FinalInterpretation
		next.Version = current.Version + 1
		next.UpdatedAt = nextTime(current.UpdatedAt)
		revision := correlation.InterpretationRevision{
			Interpretation: next,
			Event:          geology.Event{Action: "finalize", Reason: reason, Version: next.Version, At: next.UpdatedAt},
		}
		if err := appendInterpretation(state, revision); err != nil {
			return false, err
		}
		result = next
		return true, nil
	})
	return result, err
}

func (s *Service) Interpretation(ctx context.Context, id string) (correlation.Interpretation, error) {
	var result correlation.Interpretation
	err := s.repo.View(ctx, func(state persistence.State) error {
		history, exists := state.Interpretations[id]
		if !exists {
			return geology.Missing("对应解释不存在")
		}
		result = history[len(history)-1].Interpretation.Clone()
		return nil
	})
	return result, err
}

func (s *Service) InterpretationRevision(ctx context.Context, id string, version int) (correlation.InterpretationRevision, error) {
	var result correlation.InterpretationRevision
	err := s.repo.View(ctx, func(state persistence.State) error {
		history, exists := state.Interpretations[id]
		if !exists {
			return geology.Missing("对应解释不存在")
		}
		if version < 1 || version > len(history) {
			return geology.Missing("历史版本不存在")
		}
		result = history[version-1].Clone()
		return nil
	})
	return result, err
}

func (s *Service) InterpretationHistory(ctx context.Context, id string, offset, limit int) (HistoryPage, error) {
	if offset < 0 || offset > 1000000 || limit < 1 || limit > 100 {
		return HistoryPage{}, geology.Invalid("pagination", "分页参数超出范围")
	}
	result := HistoryPage{Items: []geology.Event{}, Offset: offset, Limit: limit}
	err := s.repo.View(ctx, func(state persistence.State) error {
		history, exists := state.Interpretations[id]
		if !exists {
			return geology.Missing("对应解释不存在")
		}
		result.Total = len(history)
		start := min(offset, len(history))
		end := min(start+limit, len(history))
		for i := start; i < end; i++ {
			result.Items = append(result.Items, history[i].Event)
		}
		return nil
	})
	return result, err
}

func (s *Service) Interpretations(ctx context.Context, profile string, stateFilter string, offset, limit int) (InterpretationPage, error) {
	if offset < 0 || offset > 1000000 || limit < 1 || limit > 100 {
		return InterpretationPage{}, geology.Invalid("pagination", "分页参数超出范围")
	}
	if stateFilter != "" && stateFilter != string(correlation.DraftInterpretation) && stateFilter != string(correlation.FinalInterpretation) {
		return InterpretationPage{}, geology.Invalid("state", "不支持的状态")
	}
	result := InterpretationPage{Items: []correlation.Interpretation{}, Offset: offset, Limit: limit}
	err := s.repo.View(ctx, func(state persistence.State) error {
		if profile != "" {
			if _, err := state.Latest(profile); err != nil {
				return err
			}
		}
		all := make([]correlation.Interpretation, 0)
		for _, history := range state.Interpretations {
			latest := history[len(history)-1].Interpretation
			if profile != "" && latest.Left.ID != profile && latest.Right.ID != profile {
				continue
			}
			if stateFilter != "" && string(latest.State) != stateFilter {
				continue
			}
			all = append(all, latest.Clone())
		}
		sort.Slice(all, func(i, j int) bool {
			if !all[i].UpdatedAt.Equal(all[j].UpdatedAt) {
				return all[i].UpdatedAt.After(all[j].UpdatedAt)
			}
			return all[i].ID < all[j].ID
		})
		result.Total = len(all)
		start := min(offset, len(all))
		end := min(start+limit, len(all))
		result.Items = all[start:end]
		return nil
	})
	return result, err
}
