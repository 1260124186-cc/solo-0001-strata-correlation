package geology

import (
	"strings"
	"time"
)

type AnnotationStatus string

const (
	AnnotationDraft     AnnotationStatus = "draft"
	AnnotationConfirmed AnnotationStatus = "confirmed"
	MaxAnnotations                       = 10000
	MaxNoteRevisions                     = 200
)

// AnnotationTarget 锚定一个已经锁定的历史版本。点注记只给出 DepthMM；
// 区间注记给出 TopMM/BottomMM（左闭右开）。锚点一经创建永不改变，
// 剖面后续重新打开产生新版本不会影响注记，也不会自动迁移注记。
type AnnotationTarget struct {
	ProfileID string `json:"profile_id"`
	Version   int    `json:"version"`
	Kind      string `json:"kind"`
	DepthMM   int64  `json:"depth_mm,omitempty"`
	TopMM     int64  `json:"top_mm,omitempty"`
	BottomMM  int64  `json:"bottom_mm,omitempty"`
}

// AnnotationRevision 是注记的一次不可变修订。确认状态随修订保存。
// 已确认的修订永远不会被改写；后续修改只能通过重新打开追加草拟修订，
// 以保留来龙去脉。
type AnnotationRevision struct {
	Revision int              `json:"revision"`
	Title    string           `json:"title"`
	Body     string           `json:"body"`
	Status   AnnotationStatus `json:"status"`
	Action   string           `json:"action"`
	Reason   string           `json:"reason"`
	At       time.Time        `json:"at"`
}

// Annotation 是独立于分层的资料，只通过 Target 引用历史版本，
// 任何剖面工作流都不会改写它。
type Annotation struct {
	ID        string               `json:"id"`
	Target    AnnotationTarget     `json:"target"`
	Revisions []AnnotationRevision `json:"revision_chain"`
	CreatedAt time.Time            `json:"created_at"`
	UpdatedAt time.Time            `json:"updated_at"`
}

func ValidAnnotationStatus(s AnnotationStatus) bool {
	return s == AnnotationDraft || s == AnnotationConfirmed
}

func (a Annotation) Latest() AnnotationRevision {
	return a.Revisions[len(a.Revisions)-1]
}

func (a Annotation) Clone() Annotation {
	a.Revisions = append([]AnnotationRevision{}, a.Revisions...)
	return a
}

// NormalizeAnnotationContent 规范化并校验注记文字内容。
func NormalizeAnnotationContent(title, body string) (string, string, error) {
	title = strings.TrimSpace(title)
	body = strings.TrimSpace(body)
	if err := Text("title", title, 1, 200); err != nil {
		return title, body, err
	}
	if err := Text("body", body, 1, 4000); err != nil {
		return title, body, err
	}
	return title, body, nil
}

// ValidateTarget 校验锚点，并要求它指向给定剖面确实存在的历史修订。
// 每个历史修订一经生成就不可变，因此草拟和锁定修订都可以作为锚点；
// 锚定带缺口的草拟修订时，覆盖查询仍会指出当时的缺口。
func ValidateTarget(t AnnotationTarget, p Profile) error {
	if !ValidID(t.ProfileID, "prf_") {
		return Invalid("target.profile_id", "剖面编号无效")
	}
	if t.ProfileID != p.ID {
		return Invalid("target.profile_id", "注记必须属于其锚定的剖面")
	}
	if t.Version < 1 || t.Version != p.Version {
		return Invalid("target.version", "必须指定存在的正整数历史版本")
	}
	switch t.Kind {
	case "point":
		if t.TopMM != 0 || t.BottomMM != 0 {
			return Invalid("target.interval", "点注记只需要 depth_mm")
		}
		if t.DepthMM < 0 || t.DepthMM >= p.DepthMM {
			return Invalid("target.depth_mm", "点深度必须位于剖面内，底端点不包含在区间中")
		}
	case "interval":
		if t.DepthMM != 0 {
			return Invalid("target.depth_mm", "区间注记只需要 top_mm 和 bottom_mm")
		}
		if t.TopMM < 0 || t.BottomMM <= t.TopMM || t.BottomMM > p.DepthMM {
			return Invalid("target.interval", "区间必须为正厚度且位于剖面范围内")
		}
	default:
		return Invalid("target.kind", "注记类型只能是 point 或 interval")
	}
	return nil
}

func (a Annotation) Validate() error {
	if !ValidID(a.ID, "ant_") {
		return Invalid("id", "注记编号无效")
	}
	if err := a.Target.validateShape(); err != nil {
		return err
	}
	if len(a.Revisions) == 0 || len(a.Revisions) > MaxNoteRevisions {
		return Invalid("revision_chain", "注记修订数量超出范围")
	}
	if a.CreatedAt.IsZero() || a.UpdatedAt.Before(a.CreatedAt) {
		return Invalid("time", "时间顺序无效")
	}
	var previous AnnotationRevision
	for i, rev := range a.Revisions {
		if rev.Revision != i+1 {
			return Invalid("revision_chain", "注记修订编号不连续")
		}
		if err := Text("title", rev.Title, 1, 200); err != nil {
			return err
		}
		if err := Text("body", rev.Body, 1, 4000); err != nil {
			return err
		}
		if !ValidAnnotationStatus(rev.Status) {
			return Invalid("status", "未知的确认状态")
		}
		if err := Text("reason", rev.Reason, 1, 500); err != nil {
			return err
		}
		if rev.At.IsZero() {
			return Invalid("time", "修订时间无效")
		}
		if i == 0 {
			if rev.Action != "create" || rev.Status != AnnotationDraft {
				return Invalid("revision_chain", "注记必须从草拟修订开始")
			}
			if !rev.At.Equal(a.CreatedAt) {
				return Invalid("time", "注记创建时间不一致")
			}
		} else {
			if rev.At.Before(previous.At) {
				return Invalid("time", "注记修订时间顺序无效")
			}
			switch rev.Action {
			case "revise":
				if previous.Status != AnnotationDraft {
					return Invalid("revision_chain", "只能修订草拟中的注记")
				}
				if rev.Status != AnnotationDraft {
					return Invalid("revision_chain", "内容修订保持草拟状态")
				}
			case "confirm":
				if previous.Status != AnnotationDraft || rev.Status != AnnotationConfirmed {
					return Invalid("revision_chain", "只能确认草拟中的注记")
				}
			case "reopen":
				if previous.Status != AnnotationConfirmed || rev.Status != AnnotationDraft {
					return Invalid("revision_chain", "只能重新打开已确认注记为草拟")
				}
			default:
				return Invalid("revision_chain", "未知的注记修订动作")
			}
		}
		previous = rev
	}
	if !a.Latest().At.Equal(a.UpdatedAt) {
		return Invalid("time", "注记更新时间不一致")
	}
	return nil
}

func (t AnnotationTarget) validateShape() error {
	if !ValidID(t.ProfileID, "prf_") {
		return Invalid("target.profile_id", "剖面编号无效")
	}
	if t.Version < 1 {
		return Invalid("target.version", "必须指定正整数历史版本")
	}
	switch t.Kind {
	case "point":
		if t.DepthMM < 0 || t.DepthMM > MaxDepth {
			return Invalid("target.depth_mm", "点深度超出范围")
		}
	case "interval":
		if t.TopMM < 0 || t.BottomMM <= t.TopMM || t.BottomMM > MaxDepth {
			return Invalid("target.interval", "区间深度超出范围")
		}
	default:
		return Invalid("target.kind", "注记类型只能是 point 或 interval")
	}
	return nil
}

// CoveredSegment 指出注记目标落在分层还是缺口中。区间注记按锚定版本
// 的分层边界切分；点注记恰好属于一个分层或缺口，端点深度记为零厚度段。
type CoveredSegment struct {
	TopMM    int64  `json:"top_mm"`
	BottomMM int64  `json:"bottom_mm"`
	Kind     string `json:"kind"`
	Layer    *Layer `json:"layer,omitempty"`
}

// AnnotationCoverage 描述注记在其锚定历史版本中覆盖到的分层与缺口。
// 计算只读取分层，不会改写任何剖面数据。
type AnnotationCoverage struct {
	Segments []CoveredSegment `json:"segments"`
	Gaps     []Interval       `json:"gaps"`
}

type AnnotationView struct {
	Annotation
	Coverage AnnotationCoverage `json:"coverage"`
}

// AnnotationCoverageOf 根据锚定版本的分层实时计算覆盖情况。
func AnnotationCoverageOf(t AnnotationTarget, p Profile) AnnotationCoverage {
	if t.Kind == "point" {
		return pointCoverage(t, p)
	}
	result := AnnotationCoverage{Segments: []CoveredSegment{}, Gaps: []Interval{}}
	type span struct {
		top, bottom int64
		gap         bool
		layer       *Layer
	}
	spans := make([]span, 0, len(p.Layers)*2+1)
	var end int64
	for i := range p.Layers {
		layer := &p.Layers[i]
		if layer.TopMM > end {
			spans = append(spans, span{end, layer.TopMM, true, nil})
		}
		spans = append(spans, span{layer.TopMM, layer.BottomMM, false, layer})
		end = layer.BottomMM
	}
	if end < p.DepthMM {
		spans = append(spans, span{end, p.DepthMM, true, nil})
	}
	for _, sp := range spans {
		top, bottom := max(t.TopMM, sp.top), min(t.BottomMM, sp.bottom)
		if top >= bottom {
			continue
		}
		segment := CoveredSegment{TopMM: top, BottomMM: bottom}
		if sp.gap {
			segment.Kind = "gap"
			result.Gaps = append(result.Gaps, Interval{TopMM: top, BottomMM: bottom})
		} else {
			segment.Kind = "layer"
			layer := *sp.layer
			segment.Layer = &layer
		}
		result.Segments = append(result.Segments, segment)
	}
	return result
}

// pointCoverage 沿用深度查询的左闭右开规则：边界点属于其下方分层。
// 缺口返回其所在缺口的真实范围，便于指出具体缺口。
func pointCoverage(t AnnotationTarget, p Profile) AnnotationCoverage {
	point, err := AtDepth(p, t.DepthMM)
	if err != nil {
		return AnnotationCoverage{Segments: []CoveredSegment{}, Gaps: []Interval{}}
	}
	segment := CoveredSegment{TopMM: t.DepthMM, BottomMM: t.DepthMM}
	gaps := []Interval{}
	if point.Layer != nil {
		segment.Kind = "layer"
		layer := *point.Layer
		segment.Layer = &layer
	} else {
		segment.Kind = "gap"
		if point.Gap != nil {
			gaps = append(gaps, *point.Gap)
		}
	}
	return AnnotationCoverage{Segments: []CoveredSegment{segment}, Gaps: gaps}
}

// ViewOf 组装注记与其锚定版本的覆盖信息。
func ViewOf(a Annotation, p Profile) AnnotationView {
	return AnnotationView{
		Annotation: a.Clone(),
		Coverage:   AnnotationCoverageOf(a.Target, p),
	}
}
