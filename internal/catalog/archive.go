package catalog

import (
	"context"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/geology"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/persistence"
)

type ArchiveVersions struct {
	ThroughVersion int `json:"through_version"`
}

type ArchiveResult struct {
	ID               string `json:"id"`
	ArchivedTotal    int    `json:"archived_total"`
	FirstLiveVersion int    `json:"first_live_version"`
	LatestVersion    int    `json:"latest_version"`
}

// Archive moves the oldest contiguous prefix of revisions (versions 1..N,
// N = through_version) out of the regular history into the archive store.
// Version numbers are data, not positions, so nothing is renumbered: the
// archived prefix and the remaining live history still form one chain.
// The latest revision always stays live because every write goes through it.
func (s *Service) Archive(ctx context.Context, id string, input ArchiveVersions) (ArchiveResult, error) {
	if input.ThroughVersion < 1 {
		return ArchiveResult{}, geology.Invalid("through_version", "必须为正整数")
	}
	var result ArchiveResult
	err := s.repo.Update(ctx, func(state *persistence.State) (bool, error) {
		history, ok := state.Histories[id]
		if !ok {
			return false, geology.Missing("剖面不存在")
		}
		archived := state.Archived[id]
		total := len(archived) + len(history)
		if input.ThroughVersion > total {
			return false, geology.Invalid("through_version", "超出已有历史版本范围")
		}
		if input.ThroughVersion <= len(archived) {
			return false, geology.Conflict("指定版本及更早版本已经归档")
		}
		if input.ThroughVersion == total {
			return false, geology.Conflict("最新版本必须保留在历史中，不能归档")
		}
		move := input.ThroughVersion - len(archived)
		state.Archived[id] = append(archived, history[:move]...)
		state.Histories[id] = history[move:]
		result = ArchiveResult{ID: id, ArchivedTotal: input.ThroughVersion, FirstLiveVersion: input.ThroughVersion + 1, LatestVersion: total}
		return true, nil
	})
	return result, err
}
