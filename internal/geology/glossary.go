package geology

import (
	"sort"
	"strings"
)

// GlossaryEntry is a canonical marker-bed term imported from an external
// word list. Key is derived from the canonical name with MarkerKey and is
// the merge identity; equivalence itself never depends on the glossary.
type GlossaryEntry struct {
	Key           string `json:"key"`
	CanonicalName string `json:"canonical_name"`
	Note          string `json:"note"`
}

// GlossaryInput is one entry of an external word-list merge request.
type GlossaryInput struct {
	CanonicalName string `json:"canonical_name"`
	Note          string `json:"note"`
}

// NormalizeGlossaryInput trims, validates and keys one external entry.
func NormalizeGlossaryInput(in GlossaryInput) (GlossaryEntry, error) {
	name := strings.TrimSpace(in.CanonicalName)
	note := strings.TrimSpace(in.Note)
	if err := Text("canonical_name", name, 1, MarkerNameMax); err != nil {
		return GlossaryEntry{}, err
	}
	if err := Text("note", note, 0, 200); err != nil {
		return GlossaryEntry{}, err
	}
	return GlossaryEntry{Key: MarkerKey(name), CanonicalName: name, Note: note}, nil
}

// ValidateGlossary checks a persisted glossary map for shape and capacity.
func ValidateGlossary(entries map[string]GlossaryEntry) error {
	if entries == nil {
		return Invalid("marker_glossary", "词条表缺失")
	}
	if len(entries) > MaxGlossaryEntries {
		return Invalid("marker_glossary", "标志层词条超过 5000 条上限")
	}
	for key, entry := range entries {
		if key == "" || key != MarkerKey(entry.CanonicalName) || entry.Key != key {
			return Invalid("marker_glossary", "词条编号与名称不一致")
		}
		if err := Text("canonical_name", entry.CanonicalName, 1, MarkerNameMax); err != nil {
			return err
		}
		if err := Text("note", entry.Note, 0, 200); err != nil {
			return err
		}
	}
	return nil
}

// SortedGlossary returns glossary entries ordered by key for stable listing.
func SortedGlossary(entries map[string]GlossaryEntry) []GlossaryEntry {
	out := make([]GlossaryEntry, 0, len(entries))
	for _, entry := range entries {
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}
