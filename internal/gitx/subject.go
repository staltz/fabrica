package gitx

import (
	"context"
	"fmt"
	"strings"
)

// Subject returns the subject line (the first line of the message) of a
// commit the clone already has. It never fetches: the dashboard calls it
// while someone waits, and a commit the cache doesn't have just has no
// subject to show.
func (r *Remote) Subject(ctx context.Context, commit string) (string, error) {
	// Only a commit hash may go to git, never something that looks like an
	// option or a ref.
	if !IsCommitish(commit) {
		return "", fmt.Errorf("%q isn't a commit hash", commit)
	}
	if !r.hasCommit(ctx, commit) {
		return "", fmt.Errorf("commit %s isn't in the cache of %s", Short(commit), r.URL)
	}
	out, err := run(ctx, r.dir, "log", "-1", "--format=%s", commit)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
