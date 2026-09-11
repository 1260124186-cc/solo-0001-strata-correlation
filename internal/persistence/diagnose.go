package persistence

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/correlation"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/geology"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
)

// 诊断区域：与正常启动校验同一批检查的四个观察面。
const (
	AreaChecksum      = "snapshot_checksum"
	AreaVersionChain  = "version_chain"
	AreaComparisonRef = "comparison_references"
	AreaRecoverable   = "recoverability"
)

// findings 只携带稳定代码、定位信息和固定说明，不包含任何剖面名称、
// 地点、说明、分层描述或标志层名称等资料正文。
const (
	codeShapeUnsupported   = "snapshot_shape_unsupported"
	codeHistoryEmpty       = "history_empty"
	codeRevisionIdentity   = "revision_identity_mismatch"
	codeRevisionInvalid    = "revision_invalid"
	codeReasonInvalid      = "revision_reason_invalid"
	codeInitialRevision    = "initial_revision_invalid"
	codeChronology         = "revision_chronology_invalid"
	codeStepDraft          = "step_requires_draft"
	codeStepLayersMeta     = "step_layers_changed_metadata"
	codeStepMetaLayers     = "step_metadata_changed_layers"
	codeStepTransition     = "step_invalid_state_transition"
	codeStepAction         = "step_unknown_action"
	codeComparisonID       = "comparison_identity_invalid"
	codeComparisonRef      = "comparison_reference_missing"
	codeComparisonReplay   = "comparison_recompute_failed"
	codeComparisonMismatch = "comparison_data_mismatch"

	codeSnapshotRead     = "snapshot_unreadable"
	codeSnapshotCapacity = "snapshot_too_large"
	codeSnapshotEnvelope = "snapshot_envelope_invalid"
	codeSnapshotChecksum = "snapshot_checksum_mismatch"
	codeSnapshotDecode   = "snapshot_data_unreadable"
)

type Finding struct {
	Code     string `json:"code"`
	Area     string `json:"area"`
	Location string `json:"location,omitempty"`
	Field    string `json:"field,omitempty"`
	Detail   string `json:"detail"`
}

type Area struct {
	Name     string    `json:"name"`
	Status   string    `json:"status"`
	Findings []Finding `json:"findings"`
}

type SnapshotInfo struct {
	Present   bool   `json:"present"`
	SizeBytes int64  `json:"size_bytes"`
	Algorithm string `json:"digest_algorithm"`
	DigestOK  *bool  `json:"digest_ok,omitempty"`
}

type Report struct {
	Mode          string         `json:"mode"`
	DataDir       string         `json:"data_dir"`
	Startable     bool           `json:"startable"`
	Snapshot      SnapshotInfo   `json:"snapshot"`
	Counts        map[string]int `json:"counts"`
	Areas         []Area         `json:"areas"`
	LeftoverTemps int            `json:"leftover_temp_files"`
	Recovery      RecoveryView   `json:"recoverability"`
	TotalFindings int            `json:"total_findings"`
}

type RecoveryView struct {
	Status          string `json:"status"`
	AutoRecoverable bool   `json:"auto_recoverable"`
	Detail          string `json:"detail"`
}

// stateFinding 是 validateFindings 的内部单元。每个结论构造的错误与
// 正常启动原本使用的错误文案逐字一致；诊断只额外附加稳定代码与定位。
type stateFinding struct {
	code     string
	location string
	field    string
	err      error
}

var findingDetail = map[string]string{
	codeShapeUnsupported:   "快照结构不受支持（schema 或顶层集合缺失）",
	codeHistoryEmpty:       "存在空版本链",
	codeRevisionIdentity:   "版本编号、事件编号或时间戳不一致",
	codeRevisionInvalid:    "历史版本未通过剖面校验",
	codeReasonInvalid:      "修订理由不合规",
	codeInitialRevision:    "首个版本必须是草拟状态的创建事件",
	codeChronology:         "版本时间顺序无效",
	codeStepDraft:          "仅草拟剖面允许分层或元数据编辑",
	codeStepLayersMeta:     "分层替换不应改动元数据",
	codeStepMetaLayers:     "元数据修订不应改动分层",
	codeStepTransition:     "锁定/重新打开的状态转换不合法",
	codeStepAction:         "未知的版本事件类型",
	codeComparisonID:       "对比结果编号或算法与请求不一致",
	codeComparisonRef:      "对比引用的历史版本不存在",
	codeComparisonReplay:   "按引用重算对比时被规则拒绝",
	codeComparisonMismatch: "对比结果与按引用重算的结果不一致",
	codeSnapshotRead:       "快照文件无法读取",
	codeSnapshotCapacity:   "快照超过 64 MiB 上限",
	codeSnapshotEnvelope:   "快照不是有效的校验封装",
	codeSnapshotChecksum:   "快照 SHA-256 校验值不匹配",
	codeSnapshotDecode:     "快照数据无法解析",
}

func detailFor(code string) string {
	if d, ok := findingDetail[code]; ok {
		return d
	}
	return "快照校验未通过"
}

// Diagnose 以与 Open 完全相同的独占锁和读取/校验步骤检查数据目录，
// 全程只读：不替换快照、不清理临时文件、不修复任何内容。
func Diagnose(dir string) (Report, error) {
	absolute, lock, err := acquireDirLock(dir)
	if err != nil {
		return Report{}, err
	}
	defer releaseDirLock(lock)

	path := filepath.Join(absolute, "strata.json")
	load := loadSnapshot(path)
	tempCount := countTempFiles(absolute)

	report := Report{
		Mode:          "diagnostic",
		DataDir:       absolute,
		Counts:        map[string]int{"profiles": 0, "revisions": 0, "comparisons": 0},
		Areas:         []Area{},
		LeftoverTemps: tempCount,
	}
	report.Snapshot = SnapshotInfo{
		Present:   load.present,
		SizeBytes: load.size,
		Algorithm: "sha256",
	}
	if load.present {
		digestOK := load.checksumOK
		report.Snapshot.DigestOK = &digestOK
		report.Counts["profiles"] = len(load.state.Histories)
		for _, history := range load.state.Histories {
			report.Counts["revisions"] += len(history)
		}
		report.Counts["comparisons"] = len(load.state.Comparisons)
	}

	hardCode := stageFindingCode(load.stage)
	checksumFindings := []Finding{}
	if hardCode != "" {
		checksumFindings = append(checksumFindings, Finding{
			Code: hardCode, Area: AreaChecksum, Detail: detailFor(hardCode),
		})
	}
	report.Areas = append(report.Areas, Area{
		Name: AreaChecksum, Status: simpleStatus(checksumFindings), Findings: checksumFindings,
	})

	// 版本链与对比引用只有在快照可解析后才能检查；读取、容量、封装或
	// 校验值阶段失败时阻断。无论成功还是 validation 阶段失败，都运行
	// 与 State.Validate 同一个 validateFindings，保证结论同源。
	chainFindings, refFindings := []Finding{}, []Finding{}
	blocked := hardCode != ""
	if !blocked {
		chainFindings, refFindings = splitStateFindings(validateFindings(load.state))
	}
	report.Areas = append(report.Areas, Area{
		Name:     AreaVersionChain,
		Status:   derivedStatus(chainFindings, blocked),
		Findings: chainFindings,
	})
	report.Areas = append(report.Areas, Area{
		Name:     AreaComparisonRef,
		Status:   derivedStatus(refFindings, blocked),
		Findings: refFindings,
	})

	if report.Startable = load.err == nil; report.Startable {
		report.Recovery = RecoveryView{
			Status: "pass", AutoRecoverable: true,
			Detail: "快照完整，正常启动可直接加载当前状态",
		}
	} else {
		report.Recovery = RecoveryView{
			Status: "fail", AutoRecoverable: false,
			Detail: "快照未通过启动校验；诊断不会自动修复，需从停止服务后的备份恢复数据目录",
		}
	}
	recoveryFindings := []Finding{}
	recoveryStatus := report.Recovery.Status
	if tempCount > 0 {
		recoveryStatus = "warn"
		recoveryFindings = append(recoveryFindings, Finding{
			Code: "leftover_temp_files", Area: AreaRecoverable,
			Detail: "存在遗留的原子写临时文件；它不参与恢复，可在服务停止后清理",
		})
	}
	report.Areas = append(report.Areas, Area{
		Name: AreaRecoverable, Status: recoveryStatus, Findings: recoveryFindings,
	})
	for _, area := range report.Areas {
		report.TotalFindings += len(area.Findings)
	}
	return report, nil
}

func stageFindingCode(stage string) string {
	switch stage {
	case stageRead:
		return codeSnapshotRead
	case stageCapacity:
		return codeSnapshotCapacity
	case stageEnvelope:
		return codeSnapshotEnvelope
	case stageChecksum:
		return codeSnapshotChecksum
	case stageDecode:
		return codeSnapshotDecode
	default:
		return ""
	}
}

func countTempFiles(dir string) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	count := 0
	for _, entry := range entries {
		if !entry.IsDir() {
			if name := entry.Name(); len(name) >= 8 && name[:8] == ".strata-" {
				count++
			}
		}
	}
	return count
}

func simpleStatus(findings []Finding) string {
	if len(findings) > 0 {
		return "fail"
	}
	return "pass"
}

func derivedStatus(findings []Finding, blocked bool) string {
	if len(findings) > 0 {
		return "fail"
	}
	if blocked {
		return "blocked"
	}
	return "pass"
}

// splitStateFindings 把同一批验证结论分到版本链和对比引用两个区域。
func splitStateFindings(items []stateFinding) (chain, refs []Finding) {
	chain, refs = []Finding{}, []Finding{}
	for _, item := range items {
		f := Finding{
			Code: item.code, Location: item.location, Field: item.field,
			Detail: detailFor(item.code),
		}
		switch item.code {
		case codeComparisonID, codeComparisonRef, codeComparisonReplay, codeComparisonMismatch:
			f.Area = AreaComparisonRef
			refs = append(refs, f)
		default:
			f.Area = AreaVersionChain
			chain = append(chain, f)
		}
	}
	return chain, refs
}

// Validate 与诊断共用 validateFindings，只回传首个结论的错误供启动使用。
// 诊断判为可启动等价于结论列表为空，因此这里必然返回 nil ——
// “诊断可启动”与“正常启动成功”由此共用同一个布尔判定。
func (s State) Validate() error {
	if findings := validateFindings(s); len(findings) > 0 {
		return findings[0].err
	}
	return nil
}

// validateFindings 执行正常启动的全部快照语义检查：形状、版本链、状态
// 转换、版本理由，以及对比引用解析与按引用重算。剖面与对比按编号排序后
// 遍历，使报告顺序稳定（原启动路径的 map 遍历顺序不影响成败判定）；
// 单个版本内部的检查顺序与原 Validate 相同。
func validateFindings(s State) []stateFinding {
	if s.Schema != 1 || s.Histories == nil || s.Comparisons == nil {
		return []stateFinding{{code: codeShapeUnsupported, err: fmt.Errorf("unsupported snapshot shape")}}
	}
	findings := []stateFinding{}
	ids := make([]string, 0, len(s.Histories))
	for id := range s.Histories {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		history := s.Histories[id]
		if len(history) == 0 {
			findings = append(findings, stateFinding{
				code: codeHistoryEmpty, location: id,
				err: fmt.Errorf("empty history %s", id),
			})
			continue
		}
		for i, r := range history {
			version := i + 1
			loc := id + "#v" + strconv.Itoa(version)
			if r.Profile.ID != id || r.Profile.Version != version || r.Event.Version != version || !r.Event.At.Equal(r.Profile.UpdatedAt) {
				findings = append(findings, stateFinding{
					code: codeRevisionIdentity, location: loc,
					err: fmt.Errorf("inconsistent revision %s/%d", id, version),
				})
				continue
			}
			if err := r.Profile.Validate(); err != nil {
				findings = append(findings, stateFinding{
					code: codeRevisionInvalid, location: loc, field: problemField(err),
					err: fmt.Errorf("invalid revision %s: %w", id, err),
				})
			}
			if err := geology.Text("reason", r.Event.Reason, 1, 500); err != nil {
				findings = append(findings, stateFinding{
					code: codeReasonInvalid, location: loc, field: "reason",
					err: err,
				})
			}
			if i == 0 {
				if r.Event.Action != "create" || r.Profile.State != geology.Draft {
					findings = append(findings, stateFinding{
						code: codeInitialRevision, location: loc,
						err: fmt.Errorf("invalid initial revision"),
					})
				}
				continue
			}
			before := history[i-1].Profile
			if !before.CreatedAt.Equal(r.Profile.CreatedAt) || r.Profile.UpdatedAt.Before(before.UpdatedAt) {
				findings = append(findings, stateFinding{
					code: codeChronology, location: loc,
					err: fmt.Errorf("invalid revision chronology"),
				})
			}
			if item, bad := validateStepFinding(before, r); bad {
				item.location = loc
				findings = append(findings, item)
			}
		}
	}
	cmpIDs := make([]string, 0, len(s.Comparisons))
	for id := range s.Comparisons {
		cmpIDs = append(cmpIDs, id)
	}
	sort.Strings(cmpIDs)
	for _, id := range cmpIDs {
		result := s.Comparisons[id]
		if id != result.ID || id != result.Request.Key() || result.Algorithm != correlation.Algorithm || result.CreatedAt.IsZero() {
			findings = append(findings, stateFinding{
				code: codeComparisonID, location: id,
				err: fmt.Errorf("invalid comparison identity"),
			})
			continue
		}
		a, err := s.Revision(result.Request.Left.ID, result.Request.Left.Version)
		if err != nil {
			findings = append(findings, stateFinding{code: codeComparisonRef, location: id, err: err})
			continue
		}
		b, err := s.Revision(result.Request.Right.ID, result.Request.Right.Version)
		if err != nil {
			findings = append(findings, stateFinding{code: codeComparisonRef, location: id, err: err})
			continue
		}
		computed, err := correlation.Align(a.Profile, b.Profile, result.Request, result.CreatedAt)
		if err != nil {
			findings = append(findings, stateFinding{code: codeComparisonReplay, location: id, err: err})
			continue
		}
		expected, _ := json.Marshal(computed)
		actual, _ := json.Marshal(result)
		if string(expected) != string(actual) {
			findings = append(findings, stateFinding{
				code: codeComparisonMismatch, location: id,
				err: fmt.Errorf("comparison data mismatch"),
			})
		}
	}
	return findings
}

func validateStepFinding(before geology.Profile, r geology.Revision) (stateFinding, bool) {
	after := r.Profile
	switch r.Event.Action {
	case "metadata", "layers":
		if before.State != geology.Draft || after.State != geology.Draft {
			return stateFinding{code: codeStepDraft, err: fmt.Errorf("edited sealed revision")}, true
		}
		if r.Event.Action == "layers" && before.Metadata != after.Metadata {
			return stateFinding{code: codeStepLayersMeta, err: fmt.Errorf("layers edit changed metadata")}, true
		}
		if r.Event.Action == "metadata" && !reflect.DeepEqual(before.Layers, after.Layers) {
			return stateFinding{code: codeStepMetaLayers, err: fmt.Errorf("metadata edit changed layers")}, true
		}
	case "seal", "reopen":
		expected := geology.Sealed
		if r.Event.Action == "reopen" {
			expected = geology.Draft
		}
		if after.State != expected || before.State == expected || before.Metadata != after.Metadata || !reflect.DeepEqual(before.Layers, after.Layers) {
			return stateFinding{code: codeStepTransition, err: fmt.Errorf("invalid state change")}, true
		}
	default:
		return stateFinding{code: codeStepAction, err: fmt.Errorf("unknown revision action")}, true
	}
	return stateFinding{}, false
}

func problemField(err error) string {
	var problem *geology.Problem
	if errors.As(err, &problem) && problem.Field != "" {
		return problem.Field
	}
	return ""
}
