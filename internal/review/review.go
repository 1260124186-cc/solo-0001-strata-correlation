// Package review holds comparison review records. A review thread belongs to a
// comparison result ID, but every entry is bound to the stable fingerprint of
// the exact result instance the reviewer worked from. When the same inputs are
// recomputed into a new instance (or the old one is deleted), stale threads
// stay readable and are never attached to the new instance automatically.
package review

import (
	"crypto/rand"
	"encoding/hex"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/geology"
	"strings"
	"time"
)

const (
	// Entry lifecycle states. Confirmed conclusions are immutable; the only way
	// to move past one is to append a replacing entry, which flips the target
	// to Superseded once the replacement is confirmed.
	Draft      = "draft"
	Confirmed  = "confirmed"
	Superseded = "superseded"

	Adopted  = "adopted"
	Rejected = "rejected"
	Pending  = "pending"
)

// Interval is a reviewer-marked depth range of doubt, in the comparison's
// common (left-side) coordinates.
type Interval struct {
	TopMM    int64 `json:"top_mm"`
	BottomMM int64 `json:"bottom_mm"`
}

// Basis records which result instance an entry relied on, together with the
// two historical revisions pinned at that moment.
type Basis struct {
	ResultFingerprint string              `json:"result_fingerprint"`
	ResultCreatedAt   time.Time           `json:"result_created_at"`
	Algorithm         string              `json:"algorithm"`
	Left              geology.RevisionRef `json:"left"`
	Right             geology.RevisionRef `json:"right"`
}

// Origin records a manual migration of a thread onto a new result instance.
type Origin struct {
	SourceThreadID string    `json:"source_thread_id"`
	MigratedAt     time.Time `json:"migrated_at"`
	Reviewer       string    `json:"reviewer"`
	Reason         string    `json:"reason"`
}

// Migration is recorded on the source thread when its conclusions are carried
// forward to a new result instance.
type Migration struct {
	TargetThreadID string    `json:"target_thread_id"`
	MigratedAt     time.Time `json:"migrated_at"`
	Reviewer       string    `json:"reviewer"`
	Reason         string    `json:"reason"`
}

// Entry is one review conclusion appended to a thread.
type Entry struct {
	Sequence       int        `json:"sequence"`
	Reviewer       string     `json:"reviewer"`
	Conclusion     string     `json:"conclusion"`
	DoubtIntervals []Interval `json:"doubt_intervals"`
	Adoption       string     `json:"adoption"`
	Status         string     `json:"status"`
	Basis          Basis      `json:"basis"`
	// Replaces is the sequence of the confirmed entry that a superseding entry
	// replaces. Zero for ordinary conclusions.
	Replaces int `json:"replaces,omitempty"`
	// ReplaceReason explains why a superseding entry was added.
	ReplaceReason string `json:"replace_reason,omitempty"`
	// OriginSequence is the source thread sequence this entry was copied from
	// during a manual migration. Zero for entries authored on this thread.
	OriginSequence int       `json:"origin_sequence,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
}

// Thread is an ordered chain of review conclusions for one result instance.
type Thread struct {
	ID           string      `json:"id"`
	ComparisonID string      `json:"comparison_id"`
	Fingerprint  string      `json:"fingerprint"`
	Entries      []Entry     `json:"entries"`
	CreatedAt    time.Time   `json:"created_at"`
	UpdatedAt    time.Time   `json:"updated_at"`
	Origin       *Origin     `json:"origin,omitempty"`
	Migrations   []Migration `json:"migrations,omitempty"`
}

func NewID() (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return "rev_" + hex.EncodeToString(bytes), nil
}

func ValidThreadID(id string) bool {
	return geology.ValidID(id, "rev_")
}

// ValidWritableStatus reports whether a caller may append/confirm an entry
// into this state.
func ValidWritableStatus(status string) bool {
	return status == Draft || status == Confirmed
}

func ValidAdoption(adoption string) bool {
	return adoption == Adopted || adoption == Rejected || adoption == Pending
}

// NormalizeIntervals trims and normalizes the reviewer intervals.
func NormalizeIntervals(in []Interval) []Interval {
	out := append([]Interval{}, in...)
	for i := range out {
		if out[i].TopMM > out[i].BottomMM {
			out[i].TopMM, out[i].BottomMM = out[i].BottomMM, out[i].TopMM
		}
	}
	return out
}

func ValidateEntryInput(reviewer, conclusion string, intervals []Interval, adoption string) error {
	reviewer = strings.TrimSpace(reviewer)
	if err := geology.Text("reviewer", reviewer, 1, 120); err != nil {
		return err
	}
	if err := geology.Text("conclusion", conclusion, 1, 5000); err != nil {
		return err
	}
	if len(intervals) > 100 {
		return geology.Invalid("doubt_intervals", "疑点区间最多 100 个")
	}
	for _, v := range intervals {
		if v.TopMM < 0 || v.BottomMM <= v.TopMM || v.BottomMM > geology.MaxDepth {
			return geology.Invalid("doubt_intervals", "疑点区间必须为正厚度且位于一千米范围内")
		}
	}
	if !ValidAdoption(adoption) {
		return geology.Invalid("adoption", "采用状态必须为 adopted、rejected 或 pending")
	}
	return nil
}

func (e Entry) clone() Entry {
	e.DoubtIntervals = append([]Interval{}, e.DoubtIntervals...)
	return e
}

func (t Thread) Clone() Thread {
	out := t
	entries := make([]Entry, len(t.Entries))
	for i, e := range t.Entries {
		entries[i] = e.clone()
	}
	out.Entries = entries
	out.Migrations = append([]Migration{}, t.Migrations...)
	if t.Origin != nil {
		origin := *t.Origin
		out.Origin = &origin
	}
	return out
}

// Validate checks a persisted thread for internal consistency.
func (t Thread) Validate() error {
	if !ValidThreadID(t.ID) {
		return geology.Invalid("id", "复核线程编号无效")
	}
	if !geology.ValidID(t.ComparisonID, "cmp_") || len(t.Fingerprint) != 64 {
		return geology.Invalid("thread", "复核线程绑定的结果身份无效")
	}
	if len(t.Entries) == 0 {
		return geology.Invalid("entries", "复核线程必须至少包含一条结论")
	}
	if t.CreatedAt.IsZero() || t.UpdatedAt.Before(t.CreatedAt) {
		return geology.Invalid("time", "复核线程时间顺序无效")
	}
	liveConfirmed := 0
	for i, e := range t.Entries {
		if e.Sequence != i+1 {
			return geology.Invalid("entries", "结论顺序必须连续")
		}
		if e.CreatedAt.IsZero() || (i > 0 && e.CreatedAt.Before(t.Entries[i-1].CreatedAt)) {
			return geology.Invalid("time", "结论时间顺序无效")
		}
		if err := ValidateEntryInput(e.Reviewer, e.Conclusion, e.DoubtIntervals, e.Adoption); err != nil {
			return err
		}
		if strings.TrimSpace(e.Reviewer) != e.Reviewer || strings.TrimSpace(e.Conclusion) != e.Conclusion {
			return geology.Invalid("entries", "复核文字未规范化")
		}
		switch e.Status {
		case Draft, Confirmed, Superseded:
		default:
			return geology.Invalid("entries", "结论状态只能为 draft、confirmed 或 superseded")
		}
		if e.Basis.ResultFingerprint != t.Fingerprint {
			return geology.Invalid("entries", "结论依据的结果指纹与线程不一致")
		}
		if (e.OriginSequence != 0) != (t.Origin != nil) {
			return geology.Invalid("entries", "迁移来源信息不完整")
		}
		if e.OriginSequence != 0 && (e.OriginSequence < 1 || e.OriginSequence > len(t.Entries)) {
			return geology.Invalid("entries", "迁移来源结论序号无效")
		}
		if e.Replaces != 0 {
			if e.Replaces < 1 || e.Replaces >= e.Sequence {
				return geology.Invalid("replaces", "替代目标必须是此前的结论")
			}
			if err := geology.Text("replace_reason", e.ReplaceReason, 1, 500); err != nil {
				return err
			}
			target := t.Entries[e.Replaces-1]
			// A replacement is only meaningful once it is confirmed and the
			// target has actually been flipped to superseded. A waiting draft
			// replacement must still point at a live confirmed entry.
			if e.Status == Confirmed {
				if target.Status != Superseded {
					return geology.Invalid("replaces", "已确认的替代记录必须翻转原结论状态")
				}
			} else if e.Status == Draft {
				if target.Status != Confirmed {
					return geology.Invalid("replaces", "只能针对已确认且未被替代的结论草拟替代")
				}
			}
		}
		if e.Status == Confirmed {
			liveConfirmed++
		}
	}
	if liveConfirmed > 1 {
		return geology.Invalid("entries", "同一时刻只能有一条已确认且未被替代的结论")
	}
	return nil
}

// LiveConfirmed returns the sequence of the current confirmed, not-yet
// superseded entry, or 0 if there is none.
func (t Thread) LiveConfirmed() int {
	for _, e := range t.Entries {
		if e.Status == Confirmed {
			return e.Sequence
		}
	}
	return 0
}
