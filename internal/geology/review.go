package geology

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	// ReviewRulesVersion identifies the effective quality review rule set.
	ReviewRulesVersion = "rules-v1"
	// MinLayerThicknessMM is the smallest layer thickness the review accepts.
	MinLayerThicknessMM int64 = 50
)

// Reference points at one explicit profile revision.
type Reference struct {
	ID      string `json:"id"`
	Version int    `json:"version"`
}

// ReviewRequest identifies the reviewed revision and the sealed revisions
// whose marker beds are used for marker pairing.
type ReviewRequest struct {
	Target   Reference   `json:"target"`
	Partners []Reference `json:"partners"`
}

func (r Reference) valid() bool {
	return ValidID(r.ID, "prf_") && r.Version >= 1
}

func (r Reference) key() string {
	return fmt.Sprintf("%s/%d", r.ID, r.Version)
}

// CanonicalPartners returns partners sorted by id and version with duplicates
// rejected, so the same set always maps to the same review record.
func CanonicalPartners(refs []Reference) ([]Reference, error) {
	out := append([]Reference{}, refs...)
	seen := make(map[string]bool, len(out))
	for i, ref := range out {
		if !ref.valid() {
			return nil, Invalid(fmt.Sprintf("partners[%d]", i), "配对版本必须是明确的剖面版本")
		}
		if seen[ref.key()] {
			return nil, Invalid(fmt.Sprintf("partners[%d]", i), "配对版本不能重复")
		}
		seen[ref.key()] = true
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ID != out[j].ID {
			return out[i].ID < out[j].ID
		}
		return out[i].Version < out[j].Version
	})
	return out, nil
}

func (r ReviewRequest) Validate() error {
	if !r.Target.valid() {
		return Invalid("target", "审查目标必须是明确的剖面版本")
	}
	for i, partner := range r.Partners {
		if !partner.valid() {
			return Invalid(fmt.Sprintf("partners[%d]", i), "配对版本必须是明确的剖面版本")
		}
		if partner == r.Target {
			return Invalid(fmt.Sprintf("partners[%d]", i), "配对版本不能是审查目标自身")
		}
	}
	return nil
}

// Key derives the stable storage identifier from the rule set and request.
func (r ReviewRequest) Key() string {
	payload, _ := json.Marshal(struct {
		Rules   string        `json:"rules"`
		Request ReviewRequest `json:"request"`
	}{ReviewRulesVersion, r})
	sum := sha256.Sum256(payload)
	return "rvw_" + hex.EncodeToString(sum[:16])
}

// Finding is one rule violation located by depth interval. Every finding
// blocks sealing; all four rules report the same shape.
type Finding struct {
	Code     string `json:"code"`
	Field    string `json:"field"`
	TopMM    int64  `json:"top_mm"`
	BottomMM int64  `json:"bottom_mm"`
	Detail   string `json:"detail"`
}

// ReviewSummary counts findings by rule.
type ReviewSummary struct {
	Gaps            int `json:"gaps"`
	UnknownLayers   int `json:"unknown_layers"`
	ThinLayers      int `json:"thin_layers"`
	UnpairedMarkers int `json:"unpaired_markers"`
	Total           int `json:"total"`
}

// ReviewResult is the immutable conclusion for one explicit revision.
type ReviewResult struct {
	ID           string        `json:"id"`
	RulesVersion string        `json:"rules_version"`
	Target       Reference     `json:"target"`
	Partners     []Reference   `json:"partners"`
	Passed       bool          `json:"passed"`
	Findings     []Finding     `json:"findings"`
	Counts       ReviewSummary `json:"counts"`
	Reason       string        `json:"reason"`
	CreatedAt    time.Time     `json:"created_at"`
}

func (r ReviewResult) Clone() ReviewResult {
	r.Partners = append([]Reference{}, r.Partners...)
	r.Findings = append([]Finding{}, r.Findings...)
	return r
}

// Review evaluates one explicit profile revision against the locking quality
// rules. Partners must be sealed revisions; their marker beds decide whether
// marker beds in the target are paired. With no partners every marker bed is
// reported as unpaired. The result is purely derived from the inputs, which
// lets snapshot validation recompute it after a restart.
func Review(target Profile, partners []Profile, request ReviewRequest, reason string, now time.Time) (ReviewResult, error) {
	if err := request.Validate(); err != nil {
		return ReviewResult{}, err
	}
	if request.Target.ID != target.ID || request.Target.Version != target.Version {
		return ReviewResult{}, Invalid("target", "审查目标与剖面版本不一致")
	}
	if len(partners) != len(request.Partners) {
		return ReviewResult{}, Invalid("partners", "配对版本数量不一致")
	}
	pairedMarkers := make(map[string]bool)
	for i, partner := range partners {
		ref := request.Partners[i]
		if partner.ID != ref.ID || partner.Version != ref.Version {
			return ReviewResult{}, Invalid("partners", "配对版本与剖面不一致")
		}
		if partner.State != Sealed {
			return ReviewResult{}, Conflict("标志层配对版本必须已经锁定")
		}
		for _, layer := range partner.Layers {
			if layer.Marker != "" {
				pairedMarkers[strings.ToLower(layer.Marker)] = true
			}
		}
	}
	result := ReviewResult{
		ID:           request.Key(),
		RulesVersion: ReviewRulesVersion,
		Target:       request.Target,
		Partners:     append([]Reference{}, request.Partners...),
		Findings:     []Finding{},
		Reason:       reason,
		CreatedAt:    now,
	}
	for i, gap := range CoverageOf(target).Gaps {
		result.Findings = append(result.Findings, Finding{
			Code:     "gap",
			Field:    fmt.Sprintf("coverage.gaps[%d]", i),
			TopMM:    gap.TopMM,
			BottomMM: gap.BottomMM,
			Detail:   fmt.Sprintf("深度 %d 至 %d 毫米存在未覆盖缺口", gap.TopMM, gap.BottomMM),
		})
		result.Counts.Gaps++
	}
	for i, layer := range target.Layers {
		field := fmt.Sprintf("layers[%d]", i)
		if layer.Rock == Unknown {
			result.Findings = append(result.Findings, Finding{
				Code: "unknown_lithology", Field: field,
				TopMM: layer.TopMM, BottomMM: layer.BottomMM,
				Detail: "岩性为未知，锁定前需要鉴定确认",
			})
			result.Counts.UnknownLayers++
		}
		thickness := layer.BottomMM - layer.TopMM
		if thickness < MinLayerThicknessMM {
			result.Findings = append(result.Findings, Finding{
				Code: "thin_layer", Field: field,
				TopMM: layer.TopMM, BottomMM: layer.BottomMM,
				Detail: fmt.Sprintf("分层厚度仅 %d 毫米，低于 %d 毫米的最小编录厚度", thickness, MinLayerThicknessMM),
			})
			result.Counts.ThinLayers++
		}
		if layer.Marker != "" && !pairedMarkers[strings.ToLower(layer.Marker)] {
			result.Findings = append(result.Findings, Finding{
				Code: "unpaired_marker", Field: field + ".marker",
				TopMM: layer.TopMM, BottomMM: layer.BottomMM,
				Detail: fmt.Sprintf("标志层 %q 在配对的锁定版本中没有同名标志层", layer.Marker),
			})
			result.Counts.UnpairedMarkers++
		}
	}
	sort.Slice(result.Findings, func(i, j int) bool {
		a, b := result.Findings[i], result.Findings[j]
		if a.TopMM != b.TopMM {
			return a.TopMM < b.TopMM
		}
		if a.BottomMM != b.BottomMM {
			return a.BottomMM < b.BottomMM
		}
		if a.Code != b.Code {
			return a.Code < b.Code
		}
		return a.Field < b.Field
	})
	result.Counts.Total = len(result.Findings)
	result.Passed = result.Counts.Total == 0
	return result, nil
}
