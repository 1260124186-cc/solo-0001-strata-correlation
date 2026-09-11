package catalog

import (
	"context"

	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/exchange"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/geology"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/persistence"
)

func dataset(state *persistence.State) exchange.Dataset {
	return exchange.Dataset{Histories: state.Histories, Comparisons: state.Comparisons}
}

func (s *Service) CreatePackage(ctx context.Context, selections []exchange.Selection) (exchange.Envelope, bool, error) {
	var result exchange.Envelope
	reused := false
	err := s.repo.Update(ctx, func(state *persistence.State) (bool, error) {
		if state.Packages == nil {
			state.Packages = map[string]exchange.Envelope{}
		}
		normalized, err := exchange.NormalizeSelections(selections)
		if err != nil {
			return false, err
		}
		key, err := exchange.SelectionKey(normalized)
		if err != nil {
			return false, err
		}
		for _, pkg := range state.Packages {
			if pkg.SelectionKey == key {
				result = pkg
				reused = true
				return false, nil
			}
		}
		if len(state.Packages) >= 1000 {
			return false, geology.Conflict("资料交换包数量达到 1000 个上限")
		}
		pkg, err := exchange.Build(dataset(state), normalized)
		if err != nil {
			return false, err
		}
		if valid, _ := exchange.Validate(pkg); !valid {
			return false, geology.Conflict("生成的资料交换包未通过完整性校验")
		}
		if existing, collision := state.Packages[pkg.PackageID]; collision && existing.ContentDigest != pkg.ContentDigest {
			return false, geology.Conflict("资料交换包编号冲突，请重试")
		}
		state.Packages[pkg.PackageID] = pkg
		result = pkg
		return true, nil
	})
	return result, reused, err
}

func (s *Service) Package(ctx context.Context, id string) (exchange.Envelope, error) {
	var result exchange.Envelope
	err := s.repo.View(ctx, func(state persistence.State) error {
		if state.Packages == nil {
			return geology.Missing("资料交换包不存在")
		}
		pkg, exists := state.Packages[id]
		if !exists {
			return geology.Missing("资料交换包不存在")
		}
		result = pkg
		return nil
	})
	return result, err
}

func (s *Service) InspectPackage(ctx context.Context, id string) (exchange.Inspection, error) {
	var result exchange.Inspection
	err := s.repo.View(ctx, func(state persistence.State) error {
		if state.Packages == nil {
			return geology.Missing("资料交换包不存在")
		}
		pkg, exists := state.Packages[id]
		if !exists {
			return geology.Missing("资料交换包不存在")
		}
		result = exchange.Inspect(pkg, dataset(&state))
		return nil
	})
	return result, err
}

func (s *Service) InspectPackageData(ctx context.Context, pkg exchange.Envelope) exchange.Inspection {
	var result exchange.Inspection
	_ = s.repo.View(ctx, func(state persistence.State) error {
		result = exchange.Inspect(pkg, dataset(&state))
		return nil
	})
	return result
}
