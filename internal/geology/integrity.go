package geology

import (
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

// 锁定门槛规则代码。规则集合决定锁定时检查哪些完整性条件。
const (
	// RuleCoverage：分层必须完整覆盖 [0, depth_mm)，不允许缺口。
	RuleCoverage = "coverage"
	// RuleAdjacent：按深度排序后，相邻两层必须首尾相接，不允许空隙。
	RuleAdjacent = "adjacent"
	// RuleMinThickness：单层厚度不得低于门槛 min_layer_mm。
	RuleMinThickness = "min_thickness"
	// RuleMarkerPaired：带方位后缀的标志层必须成对出现。
	RuleMarkerPaired = "marker_paired"
)

// EvaluatorVersion 是规则解释器的版本号。规则语义或结论格式发生变化时递增，
// 历史结论记录其生成时的解释器版本，不会被新解释器静默改写。
const EvaluatorVersion = 1

// RuleSet 是一次锁定门槛的完整配置。配置随锁定事件一起冻结，
// 之后修改服务配置或请求体都不影响历史版本当时的解释。
type RuleSet struct {
	Rules []string `json:"rules"`
	// MinLayerMM 使用指针以区分"未提供"与显式的 0（0 非法，需要 422 而非被当作缺省）。
	MinLayerMM   *int64     `json:"min_layer_mm,omitempty"`
	MarkerGroups [][]string `json:"marker_groups,omitempty"`
}

// Finding 描述一条被命中的完整性规则，以及问题落在哪些深度区间（毫米，左闭右开）。
type Finding struct {
	Rule      string     `json:"rule"`
	Detail    string     `json:"detail"`
	Intervals []Interval `json:"intervals"`
}

// IntegrityReport 是规则集合对某一分层快照的确定性解释结果。
type IntegrityReport struct {
	Rules            []string   `json:"rules"`
	MinLayerMM       *int64     `json:"min_layer_mm,omitempty"`
	MarkerGroups     [][]string `json:"marker_groups,omitempty"`
	EvaluatorVersion int        `json:"evaluator_version"`
	Passed           bool       `json:"passed"`
	Findings         []Finding  `json:"findings"`
}

// KnownRule 判断规则代码是否受支持。
func KnownRule(rule string) bool {
	switch rule {
	case RuleCoverage, RuleAdjacent, RuleMinThickness, RuleMarkerPaired:
		return true
	}
	return false
}

// DefaultRuleSet 是未显式配置时使用的门槛，保持只检查连续覆盖的历史语义。
func DefaultRuleSet() RuleSet {
	return RuleSet{Rules: []string{RuleCoverage}}
}

func validMarkerSuffix(s string) bool {
	if s == "" || !utf8.ValidString(s) || len([]rune(s)) > 8 {
		return false
	}
	for _, r := range s {
		if r <= ' ' {
			return false
		}
	}
	return true
}

// NormalizeRuleSet 校验并规范化规则集合：规则去重排序，标志层组规范化后排序。
// 规范化结果是落盘和比较时使用的唯一形态。
func NormalizeRuleSet(rs RuleSet) (RuleSet, error) {
	out := RuleSet{}
	seen := map[string]bool{}
	for _, rule := range rs.Rules {
		if !KnownRule(rule) {
			return out, Invalid("rules", "不支持的完整性规则: "+rule)
		}
		if !seen[rule] {
			seen[rule] = true
			out.Rules = append(out.Rules, rule)
		}
	}
	if len(out.Rules) == 0 {
		return out, Invalid("rules", "至少需要启用一条完整性规则")
	}
	sort.Strings(out.Rules)

	if seen[RuleMinThickness] {
		if rs.MinLayerMM == nil {
			return out, Invalid("min_layer_mm", "启用 min_thickness 规则时必须配置最小厚度")
		}
		if *rs.MinLayerMM < 1 || *rs.MinLayerMM > MaxDepth {
			return out, Invalid("min_layer_mm", "最小单层厚度必须介于 1 和 1000000 毫米之间")
		}
		out.MinLayerMM = rs.MinLayerMM
	} else if rs.MinLayerMM != nil {
		return out, Invalid("min_layer_mm", "只有启用 min_thickness 规则时才能配置最小厚度")
	}

	if seen[RuleMarkerPaired] {
		suffixUsed := map[string]bool{}
		groups := make([][]string, 0, len(rs.MarkerGroups))
		if len(rs.MarkerGroups) == 0 {
			return out, Invalid("marker_groups", "启用 marker_paired 规则时必须配置标志层方位组")
		}
		for gi, group := range rs.MarkerGroups {
			if len(group) < 2 {
				return out, Invalid("marker_groups", "每个标志层方位组至少包含两个后缀")
			}
			normalized := append([]string{}, group...)
			for _, suffix := range normalized {
				if !validMarkerSuffix(suffix) {
					return out, Invalid(fmt.Sprintf("marker_groups[%d]", gi), "标志层后缀必须为 1 到 8 个非空白字符")
				}
				if suffixUsed[suffix] {
					return out, Invalid("marker_groups", "标志层后缀不能跨组或在组内重复: "+suffix)
				}
				suffixUsed[suffix] = true
			}
			groups = append(groups, normalized)
		}
		for i := range groups {
			sort.Strings(groups[i])
		}
		sort.Slice(groups, func(i, j int) bool { return groups[i][0] < groups[j][0] })
		out.MarkerGroups = groups
	} else if len(rs.MarkerGroups) > 0 {
		return out, Invalid("marker_groups", "只有启用 marker_paired 规则时才能配置标志层方位组")
	}
	return out, nil
}

func newFinding(rule, detail string, intervals []Interval) Finding {
	copied := append([]Interval{}, intervals...)
	return Finding{Rule: rule, Detail: detail, Intervals: copied}
}

// EvaluateIntegrity 使用给定规则集合解释一份分层快照。
// 解释过程是确定性的：同样的分层与规则集合必然得到同样的结论。
func EvaluateIntegrity(p Profile, rs RuleSet) IntegrityReport {
	report := IntegrityReport{
		Rules:            append([]string{}, rs.Rules...),
		MinLayerMM:       rs.MinLayerMM,
		MarkerGroups:     rs.MarkerGroups,
		EvaluatorVersion: EvaluatorVersion,
		Findings:         []Finding{},
	}
	enabled := map[string]bool{}
	for _, r := range rs.Rules {
		enabled[r] = true
	}
	layers := append([]Layer{}, p.Layers...)

	if enabled[RuleCoverage] {
		gaps := coverageGaps(p)
		if len(gaps) > 0 {
			report.Findings = append(report.Findings, newFinding(RuleCoverage, "分层未完整覆盖零深度到总深度", gaps))
		}
	}
	if enabled[RuleAdjacent] {
		gaps := adjacencyGaps(layers)
		if len(gaps) > 0 {
			report.Findings = append(report.Findings, newFinding(RuleAdjacent, "相邻分层之间存在空隙，必须首尾相接", gaps))
		}
	}
	if enabled[RuleMinThickness] {
		var thin []Interval
		for _, layer := range layers {
			if layer.BottomMM-layer.TopMM < *rs.MinLayerMM {
				thin = append(thin, Interval{layer.TopMM, layer.BottomMM})
			}
		}
		if len(thin) > 0 {
			detail := fmt.Sprintf("存在厚度小于 %d 毫米的分层", *rs.MinLayerMM)
			report.Findings = append(report.Findings, newFinding(RuleMinThickness, detail, thin))
		}
	}
	if enabled[RuleMarkerPaired] {
		report.Findings = append(report.Findings, markerPairFindings(layers, rs.MarkerGroups)...)
	}
	report.Passed = len(report.Findings) == 0
	return report
}

// 分层缺口，包括层间空隙和首/尾未覆盖区间。
func coverageGaps(p Profile) []Interval {
	var gaps []Interval
	var end int64
	for _, layer := range p.Layers {
		if layer.TopMM > end {
			gaps = append(gaps, Interval{end, layer.TopMM})
		}
		end = layer.BottomMM
	}
	if end < p.DepthMM {
		gaps = append(gaps, Interval{end, p.DepthMM})
	}
	return gaps
}

// 仅相邻层之间的空隙，不强制覆盖剖面顶端和底端。
func adjacencyGaps(layers []Layer) []Interval {
	var gaps []Interval
	for i := 1; i < len(layers); i++ {
		if layers[i].TopMM > layers[i-1].BottomMM {
			gaps = append(gaps, Interval{layers[i-1].BottomMM, layers[i].TopMM})
		}
	}
	return gaps
}

func stripMarkerSuffix(marker string, suffixes []string) (base, suffix string) {
	longest := ""
	for _, s := range suffixes {
		if strings.HasSuffix(marker, s) && len([]rune(s)) > len([]rune(longest)) {
			longest = s
		}
	}
	if longest == "" {
		return marker, ""
	}
	return strings.TrimSuffix(marker, longest), longest
}

func markerPairFindings(layers []Layer, groups [][]string) []Finding {
	// 每个后缀归属哪个方位组。
	groupOf := map[string]int{}
	var allSuffixes []string
	for gi, group := range groups {
		for _, suffix := range group {
			groupOf[suffix] = gi
			allSuffixes = append(allSuffixes, suffix)
		}
	}
	type pairGroup struct {
		groupIndex int
		bySuffix   map[string][]Interval
	}
	bases := map[string]*pairGroup{}
	var order []string
	var findings []Finding
	for _, layer := range layers {
		if layer.Marker == "" {
			continue
		}
		base, suffix := stripMarkerSuffix(layer.Marker, allSuffixes)
		interval := Interval{layer.TopMM, layer.BottomMM}
		if suffix == "" {
			// 不属于任何方位组的标志层无法成对，按发现收集。
			findings = append(findings, newFinding(RuleMarkerPaired,
				"标志层缺少方位后缀，无法配对: "+layer.Marker, []Interval{interval}))
			continue
		}
		gi := groupOf[suffix]
		pg, ok := bases[base]
		if !ok {
			pg = &pairGroup{groupIndex: gi, bySuffix: map[string][]Interval{}}
			bases[base] = pg
			order = append(order, base)
		} else if pg.groupIndex != gi {
			// 同一层名在不同方位组中出现，后缀归属冲突。
			findings = append(findings, newFinding(RuleMarkerPaired,
				"标志层方位组归属冲突: "+base, []Interval{interval}))
			continue
		}
		pg.bySuffix[suffix] = append(pg.bySuffix[suffix], interval)
	}
	for _, base := range order {
		pg := bases[base]
		var intervals []Interval
		// 成对要求：方位组内每种后缀出现次数相同且为正（如每段标志都有上、下两层）。
		count := -1
		paired := true
		for _, suffix := range groups[pg.groupIndex] {
			n := len(pg.bySuffix[suffix])
			intervals = append(intervals, pg.bySuffix[suffix]...)
			if count == -1 {
				count = n
			}
			if n != count || n == 0 {
				paired = false
			}
		}
		if !paired {
			sort.Slice(intervals, func(i, j int) bool {
				if intervals[i].TopMM != intervals[j].TopMM {
					return intervals[i].TopMM < intervals[j].TopMM
				}
				return intervals[i].BottomMM < intervals[j].BottomMM
			})
			findings = append(findings, newFinding(RuleMarkerPaired,
				"标志层未成对出现: "+base, intervals))
		}
	}
	sort.Slice(findings, func(i, j int) bool {
		return findings[i].Intervals[0].TopMM < findings[j].Intervals[0].TopMM
	})
	return findings
}
