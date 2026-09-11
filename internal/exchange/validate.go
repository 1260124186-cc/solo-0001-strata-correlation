package exchange

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"reflect"

	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/correlation"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/geology"
)

func supportedFormat(format string) bool {
	// When format v2 is introduced, add its migration before accepting it
	// here. Unknown future fields must remain invalid in this reader.
	return format == Format
}

func revisionFromHistory(histories map[string][]geology.Revision, id string, version int) (geology.Revision, error) {
	history, ok := histories[id]
	if !ok || !revisionExists(history, version) {
		return geology.Revision{}, geology.Missing("剖面历史版本不存在")
	}
	return history[version-1], nil
}

func expectedDigest(value any) (string, bool) {
	digest, err := digestValue(value)
	if err != nil {
		return "", false
	}
	return digest, true
}

// Validate checks the envelope itself. It never treats a format it cannot
// understand, a broken digest, or an inconsistent manifest as valid.
func Validate(e Envelope) (bool, []string) {
	var issues []string
	addIssue := func(issue string) { issues = append(issues, issue) }

	if !supportedFormat(e.Format) {
		if e.Format == "" {
			addIssue("缺少格式版本")
		} else {
			addIssue("不支持的交换包格式版本: " + e.Format)
		}
		return false, issues
	}
	if e.Payload.Format != Format || e.Manifest.Format != Format {
		addIssue("载荷或清单的格式版本不一致")
	}
	got, ok := expectedDigest(struct {
		Format   string   `json:"format"`
		Payload  Payload  `json:"payload"`
		Manifest Manifest `json:"manifest"`
	}{Format: Format, Payload: e.Payload, Manifest: e.Manifest})
	if !ok {
		addIssue("内容摘要无法计算")
	} else if got != e.ContentDigest || e.Summary.ContentDigest != got {
		addIssue("交换包内容摘要不匹配")
	}
	if expected := packageIDFromDigest(got); expected == "" || expected != e.PackageID {
		addIssue("交换包编号与内容摘要不一致")
	}
	key, err := SelectionKey(e.Payload.Selections)
	if err != nil || key != e.SelectionKey {
		addIssue("选择集合摘要不匹配")
	}
	if _, err := NormalizeSelections(e.Payload.Selections); err != nil {
		addIssue("选择清单无效")
	}

	expectedComplete := len(e.Payload.MissingReferences) == 0 && len(e.Manifest.MissingReferences) == 0
	if (e.Status != StatusComplete && e.Status != StatusIncomplete) || e.Complete != expectedComplete ||
		(expectedComplete && e.Status != StatusComplete) || (!expectedComplete && e.Status != StatusIncomplete) {
		addIssue("完整性状态与缺失引用清单不一致")
	}

	historyByID := map[string][]geology.Revision{}
	comparisonPayloads := map[string]correlation.Result{}
	comparisonByID := map[string]correlation.Result{}
	uniqueComparisons := 0
	for _, h := range e.Payload.Histories {
		if _, duplicate := historyByID[h.ProfileID]; duplicate {
			addIssue("剖面历史重复: " + h.ProfileID)
		}
		if len(h.Revisions) == 0 {
			addIssue("剖面历史为空: " + h.ProfileID)
		}
		for _, revision := range h.Revisions {
			if err := revision.Profile.Validate(); err != nil {
				addIssue("剖面版本无效: " + h.ProfileID)
				break
			}
			if revision.Profile.ID != h.ProfileID || revision.Event.Version != revision.Profile.Version || !revision.Event.At.Equal(revision.Profile.UpdatedAt) {
				addIssue("剖面事件与版本不一致: " + h.ProfileID)
				break
			}
			if err := geology.Text("reason", revision.Event.Reason, 1, 500); err != nil {
				addIssue("剖面事件理由无效: " + h.ProfileID)
				break
			}
		}
		historyByID[h.ProfileID] = append([]geology.Revision{}, h.Revisions...)
	}
	for _, result := range e.Payload.Comparisons {
		if _, duplicate := comparisonPayloads[result.ID]; duplicate {
			addIssue("对比结果重复: " + result.ID)
		}
		if result.ID != result.Request.Key() || result.Algorithm != correlation.Algorithm || result.CreatedAt.IsZero() {
			addIssue("对比结果身份无效: " + result.ID)
			continue
		}
		comparisonPayloads[result.ID] = result
		uniqueComparisons++
	}
	if uniqueComparisons != len(e.Payload.Comparisons) {
		return false, append(issues, "交换包包含重复对比结果")
	}
	for id, result := range comparisonPayloads {
		left, leftErr := revisionFromHistory(historyByID, result.Request.Left.ID, result.Request.Left.Version)
		right, rightErr := revisionFromHistory(historyByID, result.Request.Right.ID, result.Request.Right.Version)
		if leftErr != nil {
			addIssue("对比结果引用了无效的左侧剖面版本: " + id)
		}
		if rightErr != nil {
			addIssue("对比结果引用了无效的右侧剖面版本: " + id)
		}
		if leftErr != nil || rightErr != nil {
			continue
		}
		computed, err := correlation.Align(left.Profile, right.Profile, result.Request, result.CreatedAt)
		if err != nil || !jsonEqual(result, computed) {
			addIssue("对比结果无法由包内引用版本复算: " + id)
			continue
		}
		comparisonByID[id] = result
	}
	if expectedComplete && len(comparisonByID) != len(e.Payload.Comparisons) {
		return false, append(issues, "交换包包含重复或无法复算的对比结果")
	}

	if len(historyByID) != len(e.Payload.Histories) {
		return false, append(issues, "交换包包含重复剖面历史")
	}

	// Rebuild from the package's own payload. This proves the declared
	// selections really produce exactly these members and missing references,
	// independently of current service storage.
	rebuilt, err := Build(Dataset{Histories: historyByID, Comparisons: comparisonPayloads}, e.Payload.Selections)
	if err != nil {
		addIssue("无法根据包内载荷重建引用闭包: " + err.Error())
	} else {
		if !reflect.DeepEqual(rebuilt.Payload.Histories, e.Payload.Histories) {
			addIssue("剖面历史闭包与选择项不一致")
		}
		if !reflect.DeepEqual(rebuilt.Payload.Comparisons, e.Payload.Comparisons) {
			addIssue("对比结果闭包与选择项不一致")
		}
		if !reflect.DeepEqual(rebuilt.Payload.MissingReferences, e.Payload.MissingReferences) {
			addIssue("缺失引用闭包与包内记录不一致")
		}
		if !reflect.DeepEqual(rebuilt.Manifest.Members, e.Manifest.Members) {
			addIssue("清单成员不是选择闭包的确定结果")
		}
	}

	memberHistory := 0
	memberComparison := 0
	for i, member := range e.Manifest.Members {
		switch member.Kind {
		case KindProfileHistory:
			history, exists := historyByID[member.ID]
			if !exists {
				addIssue("清单引用了不存在的剖面历史: " + member.ID)
				continue
			}
			digest, _ := digestValue(history)
			if digest != member.ContentDigest {
				addIssue("剖面历史成员摘要不匹配: " + member.ID)
			}
			var summary HistorySummary
			if err := strictJSON(member.Summary, &summary); err != nil {
				addIssue("剖面历史内容摘要无法解析: " + member.ID)
			} else if summary.RevisionCount != len(history) || summary.ProfileID != member.ID || len(summary.Revisions) != len(history) || len(summary.Versions) != len(history) {
				addIssue("剖面历史内容摘要结构不匹配: " + member.ID)
			} else {
				for j, revision := range history {
					revDigest, _ := digestValue(revision)
					p := revision.Profile
					want := RevisionSummary{ProfileID: p.ID, Version: p.Version, State: p.State, Name: p.Name, Site: p.Site, DepthMM: p.DepthMM, LayerCount: len(p.Layers), UpdatedAt: p.UpdatedAt, Digest: revDigest}
					if summary.Versions[j] != p.Version || summary.Revisions[j] != want {
						addIssue("剖面版本内容摘要不匹配: " + member.ID)
						break
					}
				}
			}
			memberHistory++
		case KindComparison:
			result, exists := comparisonPayloads[member.ID]
			if !exists {
				addIssue("清单引用了不存在的对比结果: " + member.ID)
				continue
			}
			digest, _ := digestValue(result)
			if digest != member.ContentDigest {
				addIssue("对比结果成员摘要不匹配: " + member.ID)
			}
			var summary ComparisonSummary
			wantDigest := digest
			want := ComparisonSummary{ID: result.ID, Algorithm: result.Algorithm, Left: result.Request.Left, Right: result.Request.Right, OffsetMM: result.Request.OffsetMM, OverlapMM: result.OverlapMM, KnownMM: result.KnownMM, EqualMM: result.EqualMM, Similarity: result.Similarity, SegmentCount: len(result.Segments), MarkerCount: len(result.Markers), CreatedAt: result.CreatedAt, Digest: wantDigest}
			if err := strictJSON(member.Summary, &summary); err != nil || !jsonEqual(want, summary) {
				addIssue("对比结果内容摘要不匹配: " + result.ID)
			}
			memberComparison++
		default:
			addIssue("清单包含未知成员类型")
		}
		if i > 0 {
			prev := e.Manifest.Members[i-1]
			if memberRank(prev.Kind) > memberRank(member.Kind) || (prev.Kind == member.Kind && prev.ID >= member.ID) {
				addIssue("成员顺序不是确定顺序")
			}
		}
	}
	if memberHistory != len(e.Payload.Histories) || memberComparison != len(e.Payload.Comparisons) {
		addIssue("清单成员与载荷成员数量不一致")
	}

	if !reflect.DeepEqual(e.Payload.MissingReferences, e.Manifest.MissingReferences) {
		addIssue("载荷与清单中的缺失引用不一致")
	}
	if !sortIsSortedMissing(e.Payload.MissingReferences) {
		addIssue("缺失引用顺序不是确定顺序")
	}
	if !reflect.DeepEqual(e.Payload.Selections, sortedCopy(e.Payload.Selections)) {
		addIssue("选择清单顺序不是确定顺序")
	}

	if e.Summary.HistoryCount != len(e.Payload.Histories) || e.Summary.ComparisonCount != len(e.Payload.Comparisons) ||
		e.Summary.MissingReferenceCount != len(e.Payload.MissingReferences) {
		addIssue("顶层内容摘要计数不匹配")
	}
	revisionTotal := 0
	for _, h := range e.Payload.Histories {
		revisionTotal += len(h.Revisions)
	}
	if e.Summary.RevisionCount != revisionTotal {
		addIssue("顶层剖面版本计数不匹配")
	}

	return len(issues) == 0, issues
}

func memberRank(kind string) int {
	switch kind {
	case KindProfileHistory:
		return 1
	case KindComparison:
		return 2
	default:
		return 3
	}
}

func strictJSON(raw []byte, dst any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return io.ErrUnexpectedEOF
	}
	return nil
}

func jsonEqual(a, b any) bool {
	ra, err := json.Marshal(a)
	if err != nil {
		return false
	}
	rb, err := json.Marshal(b)
	if err != nil {
		return false
	}
	return bytes.Equal(ra, rb)
}

func sortIsSortedMissing(values []MissingReference) bool {
	for i := 1; i < len(values); i++ {
		if missingKey(values[i-1]) > missingKey(values[i]) {
			return false
		}
	}
	return true
}

func sortedCopy(values []Selection) []Selection {
	out := append([]Selection{}, values...)
	out, _ = NormalizeSelections(out)
	return out
}

// Decode parses one complete JSON document. Envelope summaries use concrete
// types, which causes unknown JSON fields to be rejected by Go's decoder.
func Decode(data []byte) (Envelope, error) {
	var header struct {
		Format string `json:"format"`
	}
	if err := json.Unmarshal(data, &header); err != nil {
		return Envelope{}, err
	}
	if !supportedFormat(header.Format) {
		return Envelope{}, fmt.Errorf("unsupported exchange package format: %s", header.Format)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var e Envelope
	if err := decoder.Decode(&e); err != nil {
		return Envelope{}, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err != nil {
			return Envelope{}, err
		}
		return Envelope{}, io.ErrUnexpectedEOF
	}
	return e, nil
}
