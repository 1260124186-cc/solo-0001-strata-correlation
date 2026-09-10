package catalog

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"strings"
	"time"

	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/geology"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/glossary"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/persistence"
)

type TermInput struct {
	Term        string            `json:"term"`
	Explanation string            `json:"explanation"`
	Rock        geology.Lithology `json:"rock"`
	Enabled     *bool             `json:"enabled,omitempty"`
}

type ParseInput struct {
	Items []string `json:"items"`
}

func newTermID() (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return "trm_" + hex.EncodeToString(bytes), nil
}

func normalizeTermInput(input TermInput) (glossary.Term, error) {
	term, err := glossary.Canonical(input.Term)
	if err != nil {
		return glossary.Term{}, err
	}
	explanation := strings.TrimSpace(input.Explanation)
	if err := geology.Text("explanation", explanation, 0, glossary.MaxExplanationRunes); err != nil {
		return glossary.Term{}, err
	}
	termRow := glossary.Term{Term: term, Explanation: explanation, Rock: input.Rock}
	if !glossary.ValidTarget(input.Rock) {
		return glossary.Term{}, geology.Invalid("rock", "术语必须对应具体的标准岩性")
	}
	termRow.Enabled = input.Enabled == nil || *input.Enabled
	return termRow, nil
}

// Terms returns the maintained table, sorted by normalized term then id.
func (s *Service) Terms(ctx context.Context) ([]glossary.Term, error) {
	var result []glossary.Term
	err := s.repo.View(ctx, func(state persistence.State) error {
		result = append([]glossary.Term{}, state.Glossary...)
		glossary.Sort(result)
		return nil
	})
	return result, err
}

func (s *Service) AddTerm(ctx context.Context, input TermInput) (glossary.Term, error) {
	row, err := normalizeTermInput(input)
	if err != nil {
		return glossary.Term{}, err
	}
	id, err := newTermID()
	if err != nil {
		return glossary.Term{}, err
	}
	var result glossary.Term
	err = s.repo.Update(ctx, func(state *persistence.State) (bool, error) {
		if len(state.Glossary) >= glossary.MaxTerms {
			return false, geology.Conflict("术语表达到 2000 条上限")
		}
		for _, existing := range state.Glossary {
			if existing.ID == id {
				return false, geology.Conflict("术语编号重复，请重试")
			}
			if existing.Signature() == row.Signature() {
				return false, geology.Conflict("该术语与此标准岩性的对应已存在")
			}
		}
		now := time.Now().UTC()
		row.ID = id
		row.CreatedAt = now
		row.UpdatedAt = now
		state.Glossary = append(state.Glossary, row)
		result = row
		return true, nil
	})
	return result, err
}

func (s *Service) UpdateTerm(ctx context.Context, id string, input TermInput) (glossary.Term, error) {
	row, err := normalizeTermInput(input)
	if err != nil {
		return glossary.Term{}, err
	}
	var result glossary.Term
	err = s.repo.Update(ctx, func(state *persistence.State) (bool, error) {
		index := -1
		for i, existing := range state.Glossary {
			if existing.ID == id {
				index = i
				break
			}
		}
		if index < 0 {
			return false, geology.Missing("术语不存在")
		}
		for _, other := range state.Glossary {
			if other.ID != id && other.Signature() == row.Signature() {
				return false, geology.Conflict("该术语与此标准岩性的对应已存在")
			}
		}
		previous := state.Glossary[index]
		row.ID = id
		row.CreatedAt = previous.CreatedAt
		row.UpdatedAt = nextTime(previous.UpdatedAt)
		state.Glossary[index] = row
		result = row
		return true, nil
	})
	return result, err
}

// ParseTerms resolves a batch of free-text words without persisting anything.
// Unknown and conflicting words are reported back instead of being classified.
func (s *Service) ParseTerms(ctx context.Context, items []string) (glossary.Report, error) {
	if items == nil {
		return glossary.Report{}, geology.Invalid("items", "必须提供待解析数组，空批量使用 []")
	}
	if len(items) > glossary.MaxParseItems {
		return glossary.Report{}, geology.Invalid("items", "单次最多解析 500 条")
	}
	inputs := make([]string, len(items))
	for index, raw := range items {
		canonical, err := glossary.CanonicalItem(raw, index)
		if err != nil {
			return glossary.Report{}, err
		}
		inputs[index] = canonical
	}
	var report glossary.Report
	err := s.repo.View(ctx, func(state persistence.State) error {
		report = glossary.Resolve(state.Glossary, inputs)
		return nil
	})
	return report, err
}
