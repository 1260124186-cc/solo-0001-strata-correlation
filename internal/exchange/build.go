package exchange

import (
	"encoding/json"
	"sort"

	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/correlation"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/geology"
)

// Build expands selected materials to their complete reference closure. The
// returned envelope is deterministic: map iteration and queue traversal order
// cannot affect member order, summaries, or hashes.
func Build(data Dataset, input []Selection) (Envelope, error) {
	selections, err := NormalizeSelections(input)
	if err != nil {
		return Envelope{}, err
	}

	historyIDs := map[string]bool{}
	comparisonIDs := map[string]bool{}
	missing := []MissingReference{}
	missingSeen := map[string]bool{}
	addMissing := func(m MissingReference) {
		key := missingKey(m)
		if !missingSeen[key] {
			missingSeen[key] = true
			missing = append(missing, m)
		}
	}

	// Queue items use references so each expansion can identify the material
	// that introduced it in an error/missing report.
	type job struct {
		target   Reference
		via      Reference
		relation string
	}
	var queue []job

	for _, sel := range selections {
		if sel.Type == KindProfileRevision {
			history, exists := data.Histories[sel.ID]
			ref := Reference{Kind: KindProfileRevision, ID: sel.ID, Version: sel.Version}
			if !exists {
				addMissing(MissingReference{Kind: KindProfileHistory, ID: sel.ID, ReferencedBy: Reference{Kind: "selection"}, Relation: "selected"})
				addMissing(MissingReference{Kind: KindProfileRevision, ID: sel.ID, Version: sel.Version, ReferencedBy: Reference{Kind: "selection"}, Relation: "selected"})
				continue
			}
			if !revisionExists(history, sel.Version) {
				addMissing(MissingReference{Kind: KindProfileRevision, ID: sel.ID, Version: sel.Version, ReferencedBy: Reference{Kind: "selection"}, Relation: "selected"})
			} else if history[sel.Version-1].Profile.State != geology.Sealed {
				return Envelope{}, geology.Invalid("selections", "只能选择锁定剖面版本")
			}
			if !historyIDs[sel.ID] {
				historyIDs[sel.ID] = true
				queue = append(queue, job{target: Reference{Kind: KindProfileHistory, ID: sel.ID}, via: ref, relation: "profile_revision_history"})
			}
		} else if sel.Type == KindComparison {
			if !geology.ValidID(sel.ID, "cmp_") {
				return Envelope{}, geology.Invalid("selections.id", "对比结果编号无效")
			}
			if _, exists := data.Comparisons[sel.ID]; !exists {
				addMissing(MissingReference{Kind: KindComparison, ID: sel.ID, ReferencedBy: Reference{Kind: "selection"}, Relation: "selected"})
				continue
			}
			if !comparisonIDs[sel.ID] {
				comparisonIDs[sel.ID] = true
				queue = append(queue, job{target: Reference{Kind: KindComparison, ID: sel.ID}, via: Reference{Kind: "selection"}, relation: "selected"})
			}
		}
	}

	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		if current.target.Kind == KindProfileHistory {
			_, exists := data.Histories[current.target.ID]
			if !exists {
				addMissing(MissingReference{Kind: KindProfileHistory, ID: current.target.ID, ReferencedBy: current.via, Relation: current.relation})
				continue
			}
			// A profile history owns every profile revision. Comparisons that
			// refer to any member of this family are also part of the closure.
			for _, id := range sortedComparisonIDs(data) {
				result := data.Comparisons[id]
				left := result.Request.Left
				right := result.Request.Right
				if left.ID == current.target.ID || right.ID == current.target.ID {
					if comparisonIDs[id] {
						continue
					}
					viaVersion := left.Version
					if right.ID == current.target.ID {
						viaVersion = right.Version
					}
					comparisonIDs[id] = true
					queue = append(queue, job{
						target:   Reference{Kind: KindComparison, ID: id},
						via:      Reference{Kind: KindProfileHistory, ID: current.target.ID, Version: viaVersion},
						relation: "history_comparison",
					})
				}
			}
			continue
		}

		result, exists := data.Comparisons[current.target.ID]
		if !exists {
			addMissing(MissingReference{Kind: KindComparison, ID: current.target.ID, ReferencedBy: current.via, Relation: current.relation})
			continue
		}
		for _, ref := range []struct {
			ref  correlation.Reference
			side string
		}{
			{result.Request.Left, "comparison_left"}, {result.Request.Right, "comparison_right"},
		} {
			history, historyExists := data.Histories[ref.ref.ID]
			if !historyExists {
				addMissing(MissingReference{Kind: KindProfileHistory, ID: ref.ref.ID, ReferencedBy: Reference{Kind: KindComparison, ID: result.ID}, Relation: ref.side})
				addMissing(MissingReference{Kind: KindProfileRevision, ID: ref.ref.ID, Version: ref.ref.Version, ReferencedBy: Reference{Kind: KindComparison, ID: result.ID}, Relation: ref.side})
				continue
			}
			if !revisionExists(history, ref.ref.Version) {
				addMissing(MissingReference{Kind: KindProfileRevision, ID: ref.ref.ID, Version: ref.ref.Version, ReferencedBy: Reference{Kind: KindComparison, ID: result.ID}, Relation: ref.side})
			}
			if !historyIDs[ref.ref.ID] {
				historyIDs[ref.ref.ID] = true
				queue = append(queue, job{
					target:   Reference{Kind: KindProfileHistory, ID: ref.ref.ID},
					via:      Reference{Kind: KindComparison, ID: result.ID},
					relation: ref.side + "_history",
				})
			}
		}
	}

	histories := make([]ProfileHistory, 0, len(historyIDs))
	missing = append([]MissingReference{}, missing...)
	members := make([]Member, 0, len(historyIDs)+len(comparisonIDs))
	revisionCount := 0

	historyIDList := make([]string, 0, len(historyIDs))
	for id := range historyIDs {
		historyIDList = append(historyIDList, id)
	}
	sort.Strings(historyIDList)
	for _, id := range historyIDList {
		history := append([]geology.Revision{}, data.Histories[id]...)
		histories = append(histories, ProfileHistory{ProfileID: id, Revisions: history})
		summary := HistorySummary{ProfileID: id, RevisionCount: len(history), Versions: make([]int, 0, len(history)), Revisions: make([]RevisionSummary, 0, len(history))}
		for _, revision := range history {
			p := revision.Profile
			if p.State == geology.Sealed {
				summary.SealedRevisionCount++
			}
			revDigest, err := digestValue(revision)
			if err != nil {
				return Envelope{}, err
			}
			summary.Versions = append(summary.Versions, p.Version)
			summary.Revisions = append(summary.Revisions, RevisionSummary{
				ProfileID: p.ID, Version: p.Version, State: p.State, Name: p.Name, Site: p.Site,
				DepthMM: p.DepthMM, LayerCount: len(p.Layers), UpdatedAt: p.UpdatedAt, Digest: revDigest,
			})
		}
		historyDigest, err := digestValue(history)
		if err != nil {
			return Envelope{}, err
		}
		summaryRaw, err := json.Marshal(summary)
		if err != nil {
			return Envelope{}, err
		}
		revisionCount += len(history)
		members = append(members, Member{Kind: KindProfileHistory, ID: id, ContentDigest: historyDigest, Summary: summaryRaw})
	}

	comparisons := make([]correlation.Result, 0, len(comparisonIDs))
	comparisonIDList := make([]string, 0, len(comparisonIDs))
	for id := range comparisonIDs {
		comparisonIDList = append(comparisonIDList, id)
	}
	sort.Strings(comparisonIDList)
	for _, id := range comparisonIDList {
		result := data.Comparisons[id]
		comparisons = append(comparisons, result)
		digest, err := digestValue(result)
		if err != nil {
			return Envelope{}, err
		}
		summary := ComparisonSummary{
			ID: result.ID, Algorithm: result.Algorithm, Left: result.Request.Left, Right: result.Request.Right,
			OffsetMM: result.Request.OffsetMM, OverlapMM: result.OverlapMM, KnownMM: result.KnownMM, EqualMM: result.EqualMM,
			Similarity: result.Similarity, SegmentCount: len(result.Segments), MarkerCount: len(result.Markers), CreatedAt: result.CreatedAt,
			Digest: digest,
		}
		summaryRaw, err := json.Marshal(summary)
		if err != nil {
			return Envelope{}, err
		}
		members = append(members, Member{Kind: KindComparison, ID: id, ContentDigest: digest, Summary: summaryRaw})
	}

	sort.Slice(missing, func(i, j int) bool {
		a, b := missing[i], missing[j]
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if a.ID != b.ID {
			return a.ID < b.ID
		}
		if a.Version != b.Version {
			return a.Version < b.Version
		}
		return missingKey(a) < missingKey(b)
	})

	payload := Payload{
		Format:            Format,
		Selections:        selections,
		Histories:         histories,
		Comparisons:       comparisons,
		MissingReferences: missing,
	}
	manifest := Manifest{Format: Format, Members: members, MissingReferences: missing}
	contentDigest, err := digestValue(struct {
		Format   string   `json:"format"`
		Payload  Payload  `json:"payload"`
		Manifest Manifest `json:"manifest"`
	}{Format: Format, Payload: payload, Manifest: manifest})
	if err != nil {
		return Envelope{}, err
	}
	key, err := SelectionKey(selections)
	if err != nil {
		return Envelope{}, err
	}
	complete := len(missing) == 0
	// Closure means references must keep being followed until no new nodes can
	// be reached. A queued comparison that has since disappeared is still an
	// explicitly reported missing member, not silently omitted.
	complete = complete && len(queue) == 0
	status := StatusComplete
	if !complete {
		status = StatusIncomplete
	}
	return Envelope{
		Format:       Format,
		PackageID:    packageIDFromDigest(contentDigest),
		SelectionKey: key,
		Status:       status,
		Complete:     complete,
		Summary: Summary{
			HistoryCount:          len(histories),
			RevisionCount:         revisionCount,
			ComparisonCount:       len(comparisons),
			MissingReferenceCount: len(missing),
			ContentDigest:         contentDigest,
		},
		ContentDigest: contentDigest,
		Payload:       payload,
		Manifest:      manifest,
	}, nil
}

func NormalizeSelections(input []Selection) ([]Selection, error) {
	if len(input) == 0 {
		return nil, geology.Invalid("selections", "至少选择一份资料")
	}
	if len(input) > 100 {
		return nil, geology.Invalid("selections", "一次最多选择 100 份资料")
	}
	out := append([]Selection{}, input...)
	for _, sel := range out {
		switch sel.Type {
		case KindProfileRevision:
			if !geology.ValidID(sel.ID, "prf_") || sel.Version < 1 {
				return nil, geology.Invalid("selections", "锁定剖面版本引用无效")
			}
		case KindComparison:
			if !geology.ValidID(sel.ID, "cmp_") || sel.Version != 0 {
				return nil, geology.Invalid("selections", "对比结果引用无效")
			}
		default:
			return nil, geology.Invalid("selections.type", "不支持的资料类型")
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Type != out[j].Type {
			return out[i].Type < out[j].Type
		}
		if out[i].ID != out[j].ID {
			return out[i].ID < out[j].ID
		}
		return out[i].Version < out[j].Version
	})
	unique := out[:0]
	for i, sel := range out {
		if i > 0 && out[i-1] == sel {
			continue
		}
		unique = append(unique, sel)
	}
	return unique, nil
}

// Encode is a stable JSON wire representation.
func Encode(e Envelope) ([]byte, error) {
	raw, err := json.Marshal(e)
	if err != nil {
		return nil, err
	}
	return append(raw, '\n'), nil
}
