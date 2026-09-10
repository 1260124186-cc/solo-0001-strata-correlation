package catalog

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/collection"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/geology"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/persistence"
)

type CollectionInput struct {
	Name        string              `json:"name"`
	Description string              `json:"description"`
	Members     []collection.Member `json:"members"`
}

type CollectionEdit struct {
	ExpectedVersion int `json:"expected_version"`
	CollectionInput
}

type CollectionDelete struct {
	ExpectedVersion int `json:"expected_version"`
}

type CollectionPage struct {
	Items  []collection.Collection `json:"items"`
	Total  int                     `json:"total"`
	Offset int                     `json:"offset"`
	Limit  int                     `json:"limit"`
}

// checkCollectionMembers 要求每个成员引用的历史版本存在且已经锁定。
func checkCollectionMembers(state *persistence.State, members []collection.Member) error {
	for _, m := range members {
		revision, err := state.Revision(m.ProfileID, m.Version)
		if err != nil {
			return err
		}
		if revision.Profile.State != geology.Sealed {
			return geology.Conflict("集合成员需要已经锁定的历史版本")
		}
	}
	return nil
}

func (s *Service) CreateCollection(ctx context.Context, input CollectionInput) (collection.Collection, error) {
	name, description, members, err := collection.Normalize(input.Name, input.Description, input.Members)
	if err != nil {
		return collection.Collection{}, err
	}
	id, err := newID("col_")
	if err != nil {
		return collection.Collection{}, err
	}
	now := time.Now().UTC()
	c := collection.Collection{ID: id, Name: name, Description: description, Members: members, Version: 1, CreatedAt: now, UpdatedAt: now}
	err = s.repo.Update(ctx, func(state *persistence.State) (bool, error) {
		if len(state.Collections) >= collection.MaxCollections {
			return false, geology.Conflict("最多保存 500 个集合")
		}
		if _, exists := state.Collections[id]; exists {
			return false, geology.Conflict("集合编号重复，请重试")
		}
		if err := checkCollectionMembers(state, members); err != nil {
			return false, err
		}
		state.Collections[id] = c.Clone()
		return true, nil
	})
	return c, err
}

func (s *Service) Collection(ctx context.Context, id string) (collection.Collection, error) {
	var result collection.Collection
	err := s.repo.View(ctx, func(state persistence.State) error {
		var err error
		result, err = state.Collection(id)
		return err
	})
	return result, err
}

func (s *Service) Collections(ctx context.Context, query, profileID string, offset, limit int) (CollectionPage, error) {
	if offset < 0 || offset > 1000000 || limit < 1 || limit > 100 {
		return CollectionPage{}, geology.Invalid("pagination", "分页参数超出范围")
	}
	if err := geology.Text("q", query, 0, 120); err != nil {
		return CollectionPage{}, err
	}
	result := CollectionPage{Items: []collection.Collection{}, Offset: offset, Limit: limit}
	err := s.repo.View(ctx, func(state persistence.State) error {
		if profileID != "" {
			if _, err := state.Latest(profileID); err != nil {
				return err
			}
		}
		needle := strings.ToLower(query)
		all := make([]collection.Collection, 0, len(state.Collections))
		for _, c := range state.Collections {
			if needle != "" && !strings.Contains(strings.ToLower(c.Name), needle) {
				continue
			}
			if profileID != "" {
				found := false
				for _, m := range c.Members {
					if m.ProfileID == profileID {
						found = true
						break
					}
				}
				if !found {
					continue
				}
			}
			all = append(all, c.Clone())
		}
		sort.Slice(all, func(i, j int) bool {
			if !all[i].UpdatedAt.Equal(all[j].UpdatedAt) {
				return all[i].UpdatedAt.After(all[j].UpdatedAt)
			}
			return all[i].ID < all[j].ID
		})
		result.Total = len(all)
		start := min(offset, len(all))
		end := min(start+limit, len(all))
		result.Items = all[start:end]
		return nil
	})
	return result, err
}

func (s *Service) EditCollection(ctx context.Context, id string, input CollectionEdit) (collection.Collection, error) {
	name, description, members, err := collection.Normalize(input.Name, input.Description, input.Members)
	if err != nil {
		return collection.Collection{}, err
	}
	var result collection.Collection
	err = s.repo.Update(ctx, func(state *persistence.State) (bool, error) {
		c, err := state.Collection(id)
		if err != nil {
			return false, err
		}
		if c.Version != input.ExpectedVersion {
			return false, geology.VersionConflict(input.ExpectedVersion, c.Version)
		}
		if err := checkCollectionMembers(state, members); err != nil {
			return false, err
		}
		c.Name = name
		c.Description = description
		c.Members = members
		c.Version++
		c.UpdatedAt = nextTime(c.UpdatedAt)
		state.Collections[id] = c.Clone()
		result = c
		return true, nil
	})
	return result, err
}

// DeleteCollection 只移除集合的组织关系，不影响剖面、历史版本和对比结果。
func (s *Service) DeleteCollection(ctx context.Context, id string, expectedVersion int) error {
	return s.repo.Update(ctx, func(state *persistence.State) (bool, error) {
		c, err := state.Collection(id)
		if err != nil {
			return false, err
		}
		if c.Version != expectedVersion {
			return false, geology.VersionConflict(expectedVersion, c.Version)
		}
		delete(state.Collections, id)
		return true, nil
	})
}
