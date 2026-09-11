package catalog

import (
	"context"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/persistence"
)

// Capacity reports current usage against every write limit.
type Capacity struct {
	ProfilesUsed     int `json:"profiles_used"`
	ProfilesLimit    int `json:"profiles_limit"`
	RevisionsUsed    int `json:"revisions_used"`
	RevisionsLimit   int `json:"revisions_limit"`
	ComparisonsUsed  int `json:"comparisons_used"`
	ComparisonsLimit int `json:"comparisons_limit"`
}

// SnapshotVolume reports sizes under the real on-disk file caliber. DataBytes
// is the state payload by itself; fileBytes includes the SHA-256 envelope and
// is exactly what the snapshot write limit gates on.
type SnapshotVolume struct {
	DataBytes  int  `json:"data_bytes"`
	FileBytes  int  `json:"file_bytes"`
	LimitBytes int  `json:"limit_bytes"`
	FreeBytes  int  `json:"free_bytes"`
	Full       bool `json:"full"`
}

// Diagnostics is the capacity view served by the diagnostics endpoint and uses
// the same measurement functions as the commit path.
type Diagnostics struct {
	Capacity Capacity       `json:"capacity"`
	Snapshot SnapshotVolume `json:"snapshot"`
}

func (s *Service) Diagnostics(ctx context.Context) (Diagnostics, error) {
	var report Diagnostics
	err := s.repo.View(ctx, func(state persistence.State) error {
		report.Capacity = Capacity{
			ProfilesUsed:     len(state.Histories),
			ProfilesLimit:    maxProfiles,
			RevisionsUsed:    maxRevisionsPerProfile(state),
			RevisionsLimit:   maxRevisions,
			ComparisonsUsed:  len(state.Comparisons),
			ComparisonsLimit: maxComparisons,
		}
		dataBytes, fileBytes, err := persistence.SnapshotSize(state)
		if err != nil {
			return err
		}
		free := persistence.MaxSnapshotBytes - fileBytes
		if free < 0 {
			free = 0
		}
		report.Snapshot = SnapshotVolume{
			DataBytes:  dataBytes,
			FileBytes:  fileBytes,
			LimitBytes: persistence.MaxSnapshotBytes,
			FreeBytes:  free,
			Full:       fileBytes >= persistence.MaxSnapshotBytes,
		}
		return nil
	})
	return report, err
}

func maxRevisionsPerProfile(state persistence.State) int {
	used := 0
	for _, history := range state.Histories {
		if len(history) > used {
			used = len(history)
		}
	}
	return used
}
