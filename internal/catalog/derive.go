package catalog

import (
	"context"
	"fmt"
	"time"

	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/geology"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/persistence"
)

// DerivePreviewRequest 描述一次剖面裁剪预览：来源历史版本与深度区间。
type DerivePreviewRequest struct {
	SourceID      string `json:"source_id"`
	SourceVersion int    `json:"source_version"`
	TopMM         int64  `json:"top_mm"`
	BottomMM      int64  `json:"bottom_mm"`
}

func (r DerivePreviewRequest) Validate() error {
	if !geology.ValidID(r.SourceID, "prf_") {
		return geology.Invalid("source_id", "剖面编号无效")
	}
	if r.SourceVersion < 1 || r.SourceVersion > 500 {
		return geology.Invalid("source_version", "需要指定 1 到 500 的历史版本")
	}
	if r.TopMM < 0 || r.BottomMM > geology.MaxDepth || r.TopMM >= r.BottomMM {
		return geology.Invalid("range", "裁剪区间必须为正长度且位于一千米深度上限内")
	}
	return nil
}

// DeriveConfirmRequest 在预览之后确认创建派生剖面。ExpectedSourceVersion
// 必须是预览时返回的来源当前版本，否则说明预览前提已经变化。
type DeriveConfirmRequest struct {
	SourceID              string `json:"source_id"`
	SourceVersion         int    `json:"source_version"`
	TopMM                 int64  `json:"top_mm"`
	BottomMM              int64  `json:"bottom_mm"`
	ExpectedSourceVersion int    `json:"expected_source_version"`
	Name                  string `json:"name"`
	Site                  string `json:"site"`
	Note                  string `json:"note"`
	Reason                string `json:"reason"`
}

// DerivePreview 是裁剪预览结果，只读不写入任何数据。
type DerivePreview struct {
	SourceID              string           `json:"source_id"`
	SourceVersion         int              `json:"source_version"`
	ExpectedSourceVersion int              `json:"expected_source_version"`
	TopMM                 int64            `json:"top_mm"`
	BottomMM              int64            `json:"bottom_mm"`
	DepthMM               int64            `json:"depth_mm"`
	Layers                []geology.Layer  `json:"layers"`
	Coverage              geology.Coverage `json:"coverage"`
	Markers               []string         `json:"markers"`
}

// DeriveResult 是确认创建的结果：新草拟剖面与本次派生关系。
type DeriveResult struct {
	Profile    geology.Profile    `json:"profile"`
	Derivation geology.Derivation `json:"derivation"`
}

// Lineage 是一份剖面的完整来源链，从直接来源逐级向上。
type Lineage struct {
	ProfileID string               `json:"profile_id"`
	Items     []geology.Derivation `json:"items"`
}

func (s *Service) PreviewDerivation(ctx context.Context, input DerivePreviewRequest) (DerivePreview, error) {
	if err := input.Validate(); err != nil {
		return DerivePreview{}, err
	}
	var result DerivePreview
	err := s.repo.View(ctx, func(state persistence.State) error {
		revision, err := state.Revision(input.SourceID, input.SourceVersion)
		if err != nil {
			return err
		}
		if revision.Profile.State != geology.Sealed {
			return geology.Conflict("派生需要已经锁定的历史版本")
		}
		latest, err := state.Latest(input.SourceID)
		if err != nil {
			return err
		}
		layers, err := geology.Crop(revision.Profile, input.TopMM, input.BottomMM)
		if err != nil {
			return err
		}
		depth := input.BottomMM - input.TopMM
		coverage := geology.CoverageOf(geology.Profile{Metadata: geology.Metadata{DepthMM: depth}, Layers: layers})
		markers := []string{}
		for _, layer := range layers {
			if layer.Marker != "" {
				markers = append(markers, layer.Marker)
			}
		}
		result = DerivePreview{
			SourceID:              input.SourceID,
			SourceVersion:         input.SourceVersion,
			ExpectedSourceVersion: latest.Version,
			TopMM:                 input.TopMM,
			BottomMM:              input.BottomMM,
			DepthMM:               depth,
			Layers:                layers,
			Coverage:              coverage,
			Markers:               markers,
		}
		return nil
	})
	return result, err
}

func (s *Service) ConfirmDerivation(ctx context.Context, input DeriveConfirmRequest) (DeriveResult, error) {
	slice := DerivePreviewRequest{SourceID: input.SourceID, SourceVersion: input.SourceVersion, TopMM: input.TopMM, BottomMM: input.BottomMM}
	if err := slice.Validate(); err != nil {
		return DeriveResult{}, err
	}
	if input.ExpectedSourceVersion < 1 || input.ExpectedSourceVersion > 500 {
		return DeriveResult{}, geology.Invalid("expected_source_version", "必须为预览时返回的来源当前版本")
	}
	depth := input.BottomMM - input.TopMM
	metadata, err := geology.NormalizeMetadata(geology.Metadata{Name: input.Name, Site: input.Site, DepthMM: depth, Note: input.Note})
	if err != nil {
		return DeriveResult{}, err
	}
	reason, err := normalizedReason(input.Reason)
	if err != nil {
		return DeriveResult{}, err
	}
	var result DeriveResult
	err = s.repo.Update(ctx, func(state *persistence.State) (bool, error) {
		revision, err := state.Revision(input.SourceID, input.SourceVersion)
		if err != nil {
			return false, err
		}
		if revision.Profile.State != geology.Sealed {
			return false, geology.Conflict("派生需要已经锁定的历史版本")
		}
		latest, err := state.Latest(input.SourceID)
		if err != nil {
			return false, err
		}
		if latest.Version != input.ExpectedSourceVersion {
			return false, geology.Conflict(fmt.Sprintf("预览前提已变化：来源剖面当前版本为 %d，预览时为 %d，请重新预览后再确认", latest.Version, input.ExpectedSourceVersion))
		}
		layers, err := geology.Crop(revision.Profile, input.TopMM, input.BottomMM)
		if err != nil {
			return false, err
		}
		if len(state.Histories) >= 2000 {
			return false, geology.Conflict("最多保存 2000 个剖面")
		}
		id, err := newID()
		if err != nil {
			return false, err
		}
		if _, exists := state.Histories[id]; exists {
			return false, geology.Conflict("剖面编号重复，请重试")
		}
		if err := geology.CheckDerivationLink(state.Derivations, id, input.SourceID); err != nil {
			return false, err
		}
		now := time.Now().UTC()
		p := geology.Profile{ID: id, Metadata: metadata, Layers: layers, State: geology.Draft, Version: 1, CreatedAt: now, UpdatedAt: now}
		if err := p.Validate(); err != nil {
			return false, err
		}
		derivation := geology.Derivation{DerivedID: id, SourceID: input.SourceID, SourceVersion: input.SourceVersion, TopMM: input.TopMM, BottomMM: input.BottomMM, CreatedAt: now}
		state.Histories[id] = []geology.Revision{{Profile: p, Event: geology.Event{Action: "create", Reason: reason, Version: 1, At: now}}}
		state.Derivations[id] = derivation
		result = DeriveResult{Profile: p, Derivation: derivation}
		return true, nil
	})
	return result, err
}

func (s *Service) DerivationOf(ctx context.Context, id string) (geology.Derivation, error) {
	var result geology.Derivation
	err := s.repo.View(ctx, func(state persistence.State) error {
		if _, err := state.Latest(id); err != nil {
			return err
		}
		d, ok := state.Derivations[id]
		if !ok {
			return geology.Missing("该剖面没有派生记录")
		}
		result = d
		return nil
	})
	return result, err
}

func (s *Service) Lineage(ctx context.Context, id string) (Lineage, error) {
	result := Lineage{ProfileID: id, Items: []geology.Derivation{}}
	err := s.repo.View(ctx, func(state persistence.State) error {
		if _, err := state.Latest(id); err != nil {
			return err
		}
		current := id
		for steps := 0; steps <= len(state.Derivations); steps++ {
			d, ok := state.Derivations[current]
			if !ok {
				return nil
			}
			result.Items = append(result.Items, d)
			current = d.SourceID
		}
		return fmt.Errorf("derivation cycle detected for %s", id)
	})
	return result, err
}
