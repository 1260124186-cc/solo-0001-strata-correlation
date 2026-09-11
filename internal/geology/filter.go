package geology

import (
	"sort"
	"strings"
)

type Filter struct {
	Query  string
	Site   string
	State  State
	Rock   Lithology
	Area   string
	Offset int
	Limit  int
}

type Page struct {
	Items  []Profile `json:"items"`
	Total  int       `json:"total"`
	Offset int       `json:"offset"`
	Limit  int       `json:"limit"`
}

func (f Filter) Validate() error {
	if f.State != "" && f.State != Draft && f.State != Sealed {
		return Invalid("state", "不支持的状态")
	}
	if f.Rock != "" && !ValidRock(f.Rock) {
		return Invalid("rock", "不支持的岩性")
	}
	if f.Area != "" && !ValidAreaID(f.Area) {
		return Invalid("area", "研究区编号无效")
	}
	if f.Offset < 0 || f.Offset > 1000000 {
		return Invalid("offset", "偏移量必须为 0 到 1000000")
	}
	if f.Limit < 1 || f.Limit > 100 {
		return Invalid("limit", "每页条数必须为 1 到 100")
	}
	if err := Text("q", f.Query, 0, 120); err != nil {
		return err
	}
	return Text("site", f.Site, 0, 200)
}

func Select(profiles []Profile, f Filter) Page {
	selected := make([]Profile, 0)
	for _, p := range profiles {
		if f.State != "" && p.State != f.State {
			continue
		}
		if f.Query != "" && !strings.Contains(strings.ToLower(p.Name), strings.ToLower(f.Query)) {
			continue
		}
		if f.Site != "" && !strings.Contains(strings.ToLower(p.Site), strings.ToLower(f.Site)) {
			continue
		}
		if f.Rock != "" {
			found := false
			for _, l := range p.Layers {
				if l.Rock == f.Rock {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}
		selected = append(selected, p.Clone())
	}
	sort.Slice(selected, func(i, j int) bool {
		if !selected[i].UpdatedAt.Equal(selected[j].UpdatedAt) {
			return selected[i].UpdatedAt.After(selected[j].UpdatedAt)
		}
		return selected[i].ID < selected[j].ID
	})
	total := len(selected)
	start := f.Offset
	if start > total {
		start = total
	}
	end := start + f.Limit
	if end > total {
		end = total
	}
	return Page{Items: selected[start:end], Total: total, Offset: f.Offset, Limit: f.Limit}
}
