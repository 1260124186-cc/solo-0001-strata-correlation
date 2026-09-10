package catalog

import (
	"context"
	"fmt"
	"reflect"
	"time"

	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/geology"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/importing"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/persistence"
)

// ImportRejected marks a confirmation attempt that created nothing because the
// batch failed the rules. The import itself is durably persisted as failed.
type ImportRejected struct {
	Import importing.Import
}

func (e *ImportRejected) Error() string {
	if e.Import.Failure != "" {
		return e.Import.Failure
	}
	return fmt.Sprintf("导入 %s 未通过校验，已标记为失败", e.Import.ID)
}

// CreateImport parses CSV into a persisted preview. Identical CSV always maps
// to the same import id; repeat uploads return the stored job unchanged.
func (s *Service) CreateImport(ctx context.Context, source []byte) (importing.Import, bool, error) {
	parsed, err := importing.Parse(source, time.Now().UTC())
	if err != nil {
		return importing.Import{}, false, err
	}
	var result importing.Import
	reused := false
	err = s.repo.Update(ctx, func(state *persistence.State) (bool, error) {
		if existing, ok := state.Imports[parsed.ID]; ok {
			result = existing.Clone()
			reused = true
			return false, nil
		}
		state.Imports[parsed.ID] = parsed.Clone()
		result = parsed.Clone()
		return true, nil
	})
	return result, reused, err
}

func (s *Service) GetImport(ctx context.Context, id string) (importing.Import, error) {
	if !geology.ValidID(id, "imp_") {
		return importing.Import{}, geology.Missing("导入任务不存在")
	}
	var result importing.Import
	err := s.repo.View(ctx, func(state persistence.State) error {
		job, ok := state.Imports[id]
		if !ok {
			return geology.Missing("导入任务不存在")
		}
		result = job.Clone()
		return nil
	})
	return result, err
}

// ConfirmImport creates one formal draft profile per preview group. Profiles
// are only created when the entire batch passes every rule, in one atomic
// state write. A completed or failed import never runs again.
func (s *Service) ConfirmImport(ctx context.Context, id, reason string) (importing.Import, error) {
	trimmed, err := normalizedReason(reason)
	if err != nil {
		return importing.Import{}, err
	}
	var result importing.Import
	var rejected *ImportRejected
	err = s.repo.Update(ctx, func(state *persistence.State) (bool, error) {
		job, ok := state.Imports[id]
		if !ok {
			return false, geology.Missing("导入任务不存在")
		}
		if job.Status != importing.StatusPreview {
			return false, geology.Conflict(fmt.Sprintf("导入任务已结束（%s），不能重复执行", job.Status))
		}

		// Never trust the stored preview blindly: re-derive it from the raw CSV
		// so confirmation runs the exact same rules as upload.
		reparsed, perr := importing.Parse(job.SourceCSV, job.CreatedAt)
		failure := ""
		switch {
		case perr != nil:
			failure = "原始 CSV 无法重新解析"
		case !reflect.DeepEqual(reparsed.Groups, job.Groups) || !reflect.DeepEqual(reparsed.Errors, job.Errors):
			failure = "预览与原始 CSV 不一致"
		case len(reparsed.Errors) > 0 || len(reparsed.Groups) == 0:
			failure = fmt.Sprintf("整批存在 %d 处未修正的错误，拒绝创建剖面", len(reparsed.Errors))
		}
		if failure == "" && len(state.Histories)+len(reparsed.Groups) > 2000 {
			failure = fmt.Sprintf("容量不足：现有 %d 个剖面，本批需要再创建 %d 个，上限 2000", len(state.Histories), len(reparsed.Groups))
		}
		if failure != "" {
			if perr == nil {
				// refresh preview from the exact stored CSV before rejecting it
				job.Groups = reparsed.Groups
				job.Errors = reparsed.Errors
			}
			job = markFailed(state, job, failure)
			result = job.Clone()
			rejected = &ImportRejected{Import: job.Clone()}
			return true, nil // commit the failed outcome; signaled after Update
		}

		now := time.Now().UTC()
		profiles := make([]geology.Profile, 0, len(reparsed.Groups))
		for _, group := range reparsed.Groups {
			profileID, ierr := newID()
			if ierr != nil {
				return false, ierr
			}
			layers := make([]geology.Layer, len(group.Layers))
			for i, l := range group.Layers {
				layers[i] = l.Layer
			}
			profile := geology.Profile{
				ID:        profileID,
				Metadata:  geology.Metadata{Name: group.Name, Site: group.Site, DepthMM: group.DepthMM, Note: group.Note},
				Layers:    layers,
				State:     geology.Draft,
				Version:   1,
				CreatedAt: now,
				UpdatedAt: now,
			}
			if verr := profile.Validate(); verr != nil {
				job = markFailed(state, job, fmt.Sprintf("剖面 %q 未通过既有规则：%v", group.Name, verr))
				result = job.Clone()
				rejected = &ImportRejected{Import: job.Clone()}
				return true, nil
			}
			if _, collision := state.Histories[profileID]; collision {
				job = markFailed(state, job, "剖面编号冲突，请重试")
				result = job.Clone()
				rejected = &ImportRejected{Import: job.Clone()}
				return true, nil
			}
			profiles = append(profiles, profile)
		}

		for _, profile := range profiles {
			state.Histories[profile.ID] = []geology.Revision{{
				Profile: profile,
				Event:   geology.Event{Action: "create", Reason: trimmed, Version: 1, At: now},
			}}
		}

		ids := make([]string, len(profiles))
		for i, p := range profiles {
			ids[i] = p.ID
		}
		committed := now
		job.Status = importing.StatusCompleted
		job.Failure = ""
		job.Groups = reparsed.Groups
		job.Errors = reparsed.Errors
		job.ProfileIDs = ids
		job.CommittedAt = &committed
		job.UpdatedAt = now
		state.Imports[id] = job.Clone()
		result = job.Clone()
		return true, nil
	})
	if err != nil {
		return result, err
	}
	if rejected != nil {
		return rejected.Import, rejected
	}
	return result, nil
}

// markFailed mutates the job to its terminal failed state and persists it, so
// a rejected confirmation durably records the outcome and cannot run again.
func markFailed(state *persistence.State, job importing.Import, failure string) importing.Import {
	now := time.Now().UTC()
	job.Status = importing.StatusFailed
	job.Failure = failure
	job.UpdatedAt = now
	state.Imports[job.ID] = job.Clone()
	return job
}
