package apply

import (
	"context"
	"fmt"
	"time"

	"github.com/staltz/fabrica/internal/home"
)

// Confirmed describes what `fabrica confirm` recorded.
type Confirmed struct {
	Deployment string
	Pending    home.Pending
	// Unverifiable is true when a create or update was confirmed without
	// a remote ID.
	Unverifiable bool
}

// Confirm closes a pending handoff: the person (or computer-use agent)
// followed the instructions, so the change is now real. Like apply, it
// refuses to write while another apply holds the deployment's lock.
func Confirm(ctx context.Context, homeDir, id, remoteID string, now time.Time) (*Confirmed, error) {
	r, err := OpenRepo(ctx, homeDir)
	if err != nil {
		return nil, err
	}
	if err := r.Sync(ctx); err != nil {
		return nil, err
	}
	h, err := home.Open(homeDir)
	if err != nil {
		return nil, err
	}
	name := ""
	for _, n := range h.StateNames {
		s, err := home.ReadState(homeDir, n)
		if err != nil {
			return nil, err
		}
		if _, ok := s.FindPending(id); ok {
			name = n
			break
		}
	}
	if name == "" {
		return nil, fmt.Errorf("there's no pending handoff %s in %s; `fabrica plan` lists the pending ones", id, homeDir)
	}

	result := &Confirmed{Deployment: name}
	err = r.Update(ctx, name, "fabrica: confirm "+id, func(s *home.State) error {
		if s.Lock != nil {
			return &ErrLocked{Deployment: name, Lock: *s.Lock}
		}
		i, ok := s.FindPending(id)
		if !ok {
			return fmt.Errorf("handoff %s isn't pending anymore (someone confirmed it, or an apply replaced it)", id)
		}
		p := s.Pending[i]
		result.Pending = p
		s.Pending = append(s.Pending[:i], s.Pending[i+1:]...)
		switch p.Action {
		case "create", "update":
			if p.Shape == nil {
				return fmt.Errorf("handoff %s has no shape recorded; only Fabrica should write state files", id)
			}
			res := home.Resource{
				ID: p.Resource, Runtime: p.Runtime, Routine: p.Routine, Repo: p.Repo,
				RemoteID: remoteID, Mode: "handoff", FactoryCommit: p.FactoryCommit,
				TokenHash: p.TokenHash, Shape: *p.Shape, AppliedAt: now.UTC().Truncate(time.Second),
			}
			if res.RemoteID == "" {
				res.RemoteID = p.RemoteID // an update keeps the ID it had
			}
			result.Unverifiable = res.RemoteID == ""
			upsertResource(s, res)
		case "delete":
			if remoteID != "" {
				return fmt.Errorf("handoff %s deletes a resource, so there's no new remote ID to give; run it without --remote-id", id)
			}
			removeResource(s, p.Key())
		default:
			return fmt.Errorf("handoff %s has an unknown action %q", id, p.Action)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}
