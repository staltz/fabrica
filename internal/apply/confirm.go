package apply

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/staltz/fabrica/internal/home"
)

// Confirmed describes what `fabrica confirm` recorded.
type Confirmed struct {
	Factory string
	Pending home.Pending
	// Unverifiable is true when a create or update was confirmed without
	// a remote ID.
	Unverifiable bool
	// Absent is true when an unfinished API create was confirmed as not
	// having happened, so the next apply may create it.
	Absent bool
}

// Confirm closes a pending handoff: the person (or computer-use agent)
// followed the instructions, so the change is now real. Like apply, it
// refuses to write while another apply holds the factory's lock.
func Confirm(ctx context.Context, homeDir, id, remoteID string, absent bool, now time.Time) (*Confirmed, error) {
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
	if absent && remoteID != "" {
		return nil, errors.New("pass either --remote-id or --absent, not both")
	}

	result := &Confirmed{Factory: name}
	err = r.Update(ctx, name, "fabrica: confirm "+id, func(s *home.State) error {
		if s.Lock != nil {
			return &ErrLocked{Factory: name, Lock: *s.Lock}
		}
		i, ok := s.FindPending(id)
		if !ok {
			return fmt.Errorf("handoff %s isn't pending anymore (someone confirmed it, or an apply replaced it)", id)
		}
		p := s.Pending[i]
		result.Pending = p
		s.Pending = append(s.Pending[:i], s.Pending[i+1:]...)
		if p.Uncertain {
			return confirmUncertain(s, result, p, id, remoteID, absent, now)
		}
		if absent {
			return fmt.Errorf("--absent is for an API create whose result was never recorded; %s is a handoff, so follow it and confirm it", id)
		}
		switch p.Action {
		case "create", "update":
			if p.Shape == nil {
				return fmt.Errorf("handoff %s has no shape recorded; only Fabrica should write state files", id)
			}
			res := home.Resource{
				ID: p.Resource, Runtime: p.Runtime, Routine: p.Routine, Repo: p.Repo,
				RemoteID: remoteID, Mode: "handoff", BlueprintCommit: p.BlueprintCommit,
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

// confirmUncertain closes an API create whose result was never learned.
// With a remote ID, the resource is recorded and later plans can look it
// up. With absent, nothing was created, and the next apply may try again.
// Neither means Fabrica won't guess.
func confirmUncertain(s *home.State, result *Confirmed, p home.Pending, id, remoteID string, absent bool, now time.Time) error {
	if p.Action != "create" {
		return fmt.Errorf("pending %s is marked as an unfinished create but its action is %q; only Fabrica should write state files", id, p.Action)
	}
	if absent {
		result.Absent = true
		return nil
	}
	if remoteID == "" {
		return fmt.Errorf("a create of %s on %s may have happened, and its ID was never recorded. If the resource exists, run `fabrica confirm %s --remote-id <its ID>`. If it doesn't, run `fabrica confirm %s --absent`", p.Resource, p.Runtime, id, id)
	}
	if p.Shape == nil {
		return fmt.Errorf("pending %s has no shape recorded; only Fabrica should write state files", id)
	}
	upsertResource(s, home.Resource{
		ID: p.Resource, Runtime: p.Runtime, Routine: p.Routine, Repo: p.Repo,
		RemoteID: remoteID, Mode: "api", BlueprintCommit: p.BlueprintCommit,
		TokenHash: p.TokenHash, Shape: *p.Shape, AppliedAt: now.UTC().Truncate(time.Second),
	})
	return nil
}
