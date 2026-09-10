package catalog

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/correlation"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/geology"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/persistence"
)

type BackupPage struct {
	Items  []persistence.BackupListItem `json:"items"`
	Total  int                          `json:"total"`
	Offset int                          `json:"offset"`
	Limit  int                          `json:"limit"`
}

type BackupProfileEntry struct {
	ID        string        `json:"id"`
	Name      string        `json:"name"`
	Site      string        `json:"site"`
	State     geology.State `json:"state"`
	Versions  int           `json:"versions"`
	UpdatedAt time.Time     `json:"updated_at"`
}

type BackupComparisonEntry struct {
	ID        string                `json:"id"`
	Left      correlation.Reference `json:"left"`
	Right     correlation.Reference `json:"right"`
	OffsetMM  int64                 `json:"offset_mm"`
	CreatedAt time.Time             `json:"created_at"`
}

type BackupPreview struct {
	ID          string                    `json:"id"`
	CreatedAt   time.Time                 `json:"created_at"`
	Note        string                    `json:"note,omitempty"`
	Summary     persistence.BackupSummary `json:"summary"`
	Profiles    []BackupProfileEntry      `json:"profiles"`
	Comparisons []BackupComparisonEntry   `json:"comparisons"`
}

type RestoreResult struct {
	BackupID   string                    `json:"backup_id"`
	RestoredAt time.Time                 `json:"restored_at"`
	Summary    persistence.BackupSummary `json:"summary"`
}

// CreateBackup snapshots the current state under the repository read lock,
// so the backup always contains one consistent moment without stopping
// writes for longer than the in-memory copy.
func (s *Service) CreateBackup(ctx context.Context, note string) (persistence.BackupMeta, error) {
	note = strings.TrimSpace(note)
	if err := geology.Text("note", note, 0, 500); err != nil {
		return persistence.BackupMeta{}, err
	}
	var state persistence.State
	if err := s.repo.View(ctx, func(current persistence.State) error {
		state = current
		return nil
	}); err != nil {
		return persistence.BackupMeta{}, err
	}
	return s.backups.Create(state, note)
}

func (s *Service) ListBackups(ctx context.Context, offset, limit int) (BackupPage, error) {
	if offset < 0 || offset > 1000000 || limit < 1 || limit > 100 {
		return BackupPage{}, geology.Invalid("pagination", "分页参数超出范围")
	}
	if err := ctx.Err(); err != nil {
		return BackupPage{}, err
	}
	items, err := s.backups.List()
	if err != nil {
		return BackupPage{}, err
	}
	page := BackupPage{Items: []persistence.BackupListItem{}, Total: len(items), Offset: offset, Limit: limit}
	start := min(offset, len(items))
	end := min(start+limit, len(items))
	page.Items = items[start:end]
	return page, nil
}

func (s *Service) InspectBackup(ctx context.Context, id string) (persistence.BackupDetail, error) {
	if err := ctx.Err(); err != nil {
		return persistence.BackupDetail{}, err
	}
	return s.backups.Inspect(id)
}

func (s *Service) PreviewBackup(ctx context.Context, id string) (BackupPreview, error) {
	if err := ctx.Err(); err != nil {
		return BackupPreview{}, err
	}
	state, meta, err := s.backups.Load(id)
	if err != nil {
		return BackupPreview{}, err
	}
	preview := BackupPreview{
		ID: meta.ID, CreatedAt: meta.CreatedAt, Note: meta.Note, Summary: meta.Summary,
		Profiles: []BackupProfileEntry{}, Comparisons: []BackupComparisonEntry{},
	}
	for profileID, history := range state.Histories {
		latest := history[len(history)-1].Profile
		preview.Profiles = append(preview.Profiles, BackupProfileEntry{
			ID: profileID, Name: latest.Name, Site: latest.Site,
			State: latest.State, Versions: len(history), UpdatedAt: latest.UpdatedAt,
		})
	}
	sort.Slice(preview.Profiles, func(i, j int) bool { return preview.Profiles[i].ID < preview.Profiles[j].ID })
	for _, result := range state.Comparisons {
		preview.Comparisons = append(preview.Comparisons, BackupComparisonEntry{
			ID: result.ID, Left: result.Request.Left, Right: result.Request.Right,
			OffsetMM: result.Request.OffsetMM, CreatedAt: result.CreatedAt,
		})
	}
	sort.Slice(preview.Comparisons, func(i, j int) bool { return preview.Comparisons[i].ID < preview.Comparisons[j].ID })
	return preview, nil
}

// RestoreBackup requires the caller to echo the backup identifier as an
// explicit confirmation. The validated backup replaces disk and memory
// atomically under the repository write lock; a failure before the commit
// point keeps the current data untouched.
func (s *Service) RestoreBackup(ctx context.Context, id, confirmID string) (RestoreResult, error) {
	if !persistence.ValidBackupID(id) {
		return RestoreResult{}, geology.Invalid("id", "备份编号无效")
	}
	if confirmID != id {
		return RestoreResult{}, geology.Invalid("confirm_id", "必须填入与路径一致的备份编号以确认恢复")
	}
	state, meta, err := s.backups.Load(id)
	if err != nil {
		return RestoreResult{}, err
	}
	if err = s.repo.Restore(ctx, state); err != nil {
		return RestoreResult{}, err
	}
	return RestoreResult{BackupID: meta.ID, RestoredAt: time.Now().UTC(), Summary: meta.Summary}, nil
}
