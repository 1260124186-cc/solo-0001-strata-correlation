// Package glossary maintains cataloguer lithology synonyms and resolves them
// to the fixed lithology codes used by stored strata layers. Resolution is an
// advisory, read-only operation: it never guesses a classification and never
// mutates saved layers or comparisons.
package glossary

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/geology"
)

const (
	// MaxTerms bounds the glossary inside a single snapshot.
	MaxTerms = 2000
	// MaxTermRunes is the length limit for one term or parse item.
	MaxTermRunes = 40
	// MaxExplanationRunes bounds the explanation text.
	MaxExplanationRunes = 200
	// MaxParseItems bounds one batch resolution request.
	MaxParseItems = 500
)

// Term maps one cataloguer wording to a standard lithology.
type Term struct {
	ID          string            `json:"id"`
	Term        string            `json:"term"`
	Explanation string            `json:"explanation"`
	Rock        geology.Lithology `json:"rock"`
	Enabled     bool              `json:"enabled"`
	CreatedAt   time.Time         `json:"created_at"`
	UpdatedAt   time.Time         `json:"updated_at"`
}

// Canonical returns the trimmed display form of a term with runs of whitespace
// collapsed to single spaces. Matching normalizes whitespace and case only; it
// never performs fuzzy or partial matching.
func Canonical(value string) (string, error) {
	return canonical(value, "term")
}

func canonical(value, field string) (string, error) {
	value = strings.TrimSpace(value)
	if err := geology.Text(field, value, 1, MaxTermRunes); err != nil {
		return "", err
	}
	return strings.Join(strings.Fields(value), " "), nil
}

// CanonicalItem validates and normalizes one batch-resolution input, reporting
// the error against its position in the request.
func CanonicalItem(value string, index int) (string, error) {
	return canonical(value, fmt.Sprintf("items[%d]", index))
}

// Key is the case-insensitive lookup key of a canonical term.
func Key(canonical string) string { return strings.ToLower(canonical) }

func (t Term) Key() string { return Key(t.Term) }

// TargetRocks are the standard lithologies a term may resolve to. The sentinel
// "unknown" lithology is deliberately excluded: a term must name a concrete
// rock, while unknown is the absence of a classification.
func TargetRocks() []geology.Lithology {
	rocks := make([]geology.Lithology, 0, len(geology.Rocks())-1)
	for _, rock := range geology.Rocks() {
		if rock.Code != geology.Unknown {
			rocks = append(rocks, rock.Code)
		}
	}
	return rocks
}

// ValidTarget reports whether a term may resolve to the given lithology.
func ValidTarget(rock geology.Lithology) bool {
	for _, target := range TargetRocks() {
		if rock == target {
			return true
		}
	}
	return false
}

// Validate checks a persisted term, including normalization invariants.
func (t Term) Validate() error {
	if !geology.ValidID(t.ID, "trm_") {
		return geology.Invalid("id", "术语编号无效")
	}
	canonical, err := Canonical(t.Term)
	if err != nil {
		return err
	}
	if canonical != t.Term {
		return geology.Invalid("term", "术语未规范化")
	}
	explanation := strings.TrimSpace(t.Explanation)
	if err := geology.Text("explanation", explanation, 0, MaxExplanationRunes); err != nil {
		return err
	}
	if explanation != t.Explanation {
		return geology.Invalid("explanation", "解释未规范化")
	}
	if !ValidTarget(t.Rock) {
		return geology.Invalid("rock", "术语必须对应具体的标准岩性")
	}
	if t.CreatedAt.IsZero() || t.UpdatedAt.Before(t.CreatedAt) {
		return geology.Invalid("time", "时间顺序无效")
	}
	return nil
}

// Signature identifies a unique term/rock pair. The same term may map to
// different rocks while cataloguers disagree; those rows coexist and surface
// as conflicts at resolution time.
func (t Term) Signature() string {
	return t.Key() + "\x00" + string(t.Rock)
}

// Sort orders terms by lookup key and then by id.
func Sort(terms []Term) {
	sort.Slice(terms, func(i, j int) bool {
		if terms[i].Key() != terms[j].Key() {
			return terms[i].Key() < terms[j].Key()
		}
		return terms[i].ID < terms[j].ID
	})
}

const (
	// StatusResolved means every enabled entry for the term points at one rock.
	StatusResolved = "resolved"
	// StatusUnknown means no enabled entry matches; the caller must classify manually.
	StatusUnknown = "unknown"
	// StatusConflict means enabled entries disagree on the target rock.
	StatusConflict = "conflict"
)

// Outcome is the per-item resolution result.
type Outcome struct {
	Index   int                 `json:"index"`
	Input   string              `json:"input"`
	Status  string              `json:"status"`
	Rock    geology.Lithology   `json:"rock,omitempty"`
	Rocks   []geology.Lithology `json:"rocks,omitempty"`
	TermIDs []string            `json:"term_ids"`
}

// Report summarizes one batch resolution.
type Report struct {
	Items     []Outcome `json:"items"`
	Total     int       `json:"total"`
	Resolved  int       `json:"resolved"`
	Unknown   int       `json:"unknown"`
	Conflicts int       `json:"conflicts"`
}

// Resolve matches already-validated inputs against enabled terms. Disabled
// terms never match. It reports unknown and conflicting words instead of
// guessing a classification.
func Resolve(terms []Term, inputs []string) Report {
	byKey := make(map[string][]Term)
	for _, term := range terms {
		if term.Enabled {
			byKey[term.Key()] = append(byKey[term.Key()], term)
		}
	}
	report := Report{Items: make([]Outcome, 0, len(inputs)), Total: len(inputs)}
	for index, input := range inputs {
		outcome := Outcome{Index: index, Input: input, TermIDs: []string{}}
		matches := byKey[Key(input)]
		rockSet := make(map[geology.Lithology]bool)
		for _, match := range matches {
			rockSet[match.Rock] = true
			outcome.TermIDs = append(outcome.TermIDs, match.ID)
		}
		sort.Strings(outcome.TermIDs)
		switch {
		case len(matches) == 0:
			outcome.Status = StatusUnknown
			report.Unknown++
		case len(rockSet) == 1:
			outcome.Status = StatusResolved
			outcome.Rock = matches[0].Rock
			report.Resolved++
		default:
			outcome.Status = StatusConflict
			for rock := range rockSet {
				outcome.Rocks = append(outcome.Rocks, rock)
			}
			sort.Slice(outcome.Rocks, func(i, j int) bool { return outcome.Rocks[i] < outcome.Rocks[j] })
			report.Conflicts++
		}
		report.Items = append(report.Items, outcome)
	}
	return report
}
