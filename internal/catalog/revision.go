package catalog

import (
	"context"
	"strings"
	"time"

	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/geology"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/persistence"
)

// profileRevision expresses one business action on the revision chain:
// metadata edit, layer replacement, sealing or reopening.
//
// prepare validates and normalizes request input before the history is read.
// revise enforces the action's own preconditions against the current profile
// and applies its changes, returning the history event action. It must not
// touch Version or UpdatedAt: the pipeline owns version and time progression.
type profileRevision interface {
	prepare() error
	reason() string
	revise(p *geology.Profile) (action string, err error)
}

// commitRevision runs the shared revision chain for every mutating profile
// action: load the latest revision, enforce the expected version, let the
// action apply its rules, then advance version and time, record the history
// event and append the revision through the common persistence constraints.
func (s *Service) commitRevision(ctx context.Context, id string, expected int, rev profileRevision) (geology.Profile, error) {
	if err := rev.prepare(); err != nil {
		return geology.Profile{}, err
	}
	var result geology.Profile
	err := s.repo.Update(ctx, func(state *persistence.State) (bool, error) {
		p, err := state.Latest(id)
		if err != nil {
			return false, err
		}
		if p.Version != expected {
			return false, geology.VersionConflict(expected, p.Version)
		}
		action, err := rev.revise(&p)
		if err != nil {
			return false, err
		}
		p.Version++
		p.UpdatedAt = nextTime(p.UpdatedAt)
		revision := geology.Revision{Profile: p, Event: geology.Event{Action: action, Reason: rev.reason(), Version: p.Version, At: p.UpdatedAt}}
		if err = appendRevision(state, revision); err != nil {
			return false, err
		}
		result = p
		return true, nil
	})
	return result, err
}

func nextTime(previous time.Time) time.Time {
	now := time.Now().UTC()
	if now.Before(previous) {
		return previous
	}
	return now
}

func normalizedReason(reason string) (string, error) {
	reason = strings.TrimSpace(reason)
	return reason, geology.Text("reason", reason, 1, 500)
}

// appendRevision enforces the common persistence constraints shared by every
// revision: per-profile history capacity, contiguous version numbers, profile
// validity and an isolated clone stored in the snapshot.
func appendRevision(state *persistence.State, revision geology.Revision) error {
	history := state.Histories[revision.Profile.ID]
	if len(history) >= 500 {
		return geology.Conflict("单个剖面最多保留 500 个版本")
	}
	if revision.Profile.Version != len(history)+1 {
		return geology.Conflict("版本顺序不一致")
	}
	if err := revision.Profile.Validate(); err != nil {
		return err
	}
	state.Histories[revision.Profile.ID] = append(history, revision.Clone())
	return nil
}

// --- metadata edit ---

type metadataRevision struct {
	metadata geology.Metadata
	why      string
}

func (r *metadataRevision) prepare() error {
	normalized, err := geology.NormalizeMetadata(r.metadata)
	if err != nil {
		return err
	}
	r.metadata = normalized
	r.why, err = normalizedReason(r.why)
	return err
}

func (r *metadataRevision) reason() string { return r.why }

func (r *metadataRevision) revise(p *geology.Profile) (string, error) {
	if err := geology.RequireDraft(*p); err != nil {
		return "", err
	}
	p.Metadata = r.metadata
	return "metadata", nil
}

// --- layer replacement ---

type layersRevision struct {
	layers []geology.Layer
	why    string
}

func (r *layersRevision) prepare() error {
	if r.layers == nil {
		return geology.Invalid("layers", "必须提供分层数组，清空时使用 []")
	}
	var err error
	r.why, err = normalizedReason(r.why)
	return err
}

func (r *layersRevision) reason() string { return r.why }

func (r *layersRevision) revise(p *geology.Profile) (string, error) {
	if err := geology.RequireDraft(*p); err != nil {
		return "", err
	}
	layers, err := geology.NormalizeLayers(r.layers, p.DepthMM)
	if err != nil {
		return "", err
	}
	p.Layers = layers
	return "layers", nil
}

// --- seal / reopen ---

type stateRevision struct {
	target geology.State
	why    string
}

func (r *stateRevision) prepare() error {
	var err error
	r.why, err = normalizedReason(r.why)
	return err
}

func (r *stateRevision) reason() string { return r.why }

func (r *stateRevision) revise(p *geology.Profile) (string, error) {
	action, err := geology.StateTransitionAction(*p, r.target)
	if err != nil {
		return "", err
	}
	p.State = r.target
	return action, nil
}
