//go:build !unix

package gitx

import (
	"context"
	"errors"
	"os"
	"time"
)

// lockFile takes an exclusive lock on path by creating it, waiting until
// it's free or ctx is done. This is the simple fallback for systems without
// flock. A lock older than ten minutes is treated as left behind by a
// crashed process and taken over.
func lockFile(ctx context.Context, path string) (unlock func(), err error) {
	for {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			f.Close()
			return func() { _ = os.Remove(path) }, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		if info, statErr := os.Stat(path); statErr == nil && time.Since(info.ModTime()) > 10*time.Minute {
			_ = os.Remove(path)
			continue
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}
