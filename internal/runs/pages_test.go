package runs

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"
)

// smallPages is a Lister that never gives more than two runs at a time,
// like a server with a lower limit than we asked for.
type smallPages struct {
	db    *DB
	calls int
}

func (s *smallPages) List(ctx context.Context, f Filter) ([]Summary, string, error) {
	s.calls++
	f.Limit = min(f.Limit, 2)
	return s.db.List(ctx, f)
}

func TestListAll(t *testing.T) {
	db := openTest(t)
	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	var want []string
	for i := range 7 {
		at := base.Add(time.Duration(i) * time.Minute)
		want = append(want, put(t, db, seed{Run: Run{ID: NewID(at), StartedAt: at}})...)
	}
	slices.Reverse(want) // newest first

	ids := func(list []Summary) []string {
		out := []string{}
		for _, s := range list {
			out = append(out, s.RunID)
		}
		return out
	}
	ctx := context.Background()
	for _, c := range []struct {
		name     string
		l        Lister
		limit    int
		want     []string
		wantNext string
	}{
		{"one page", db, 5, want[:5], want[4]},
		{"all of them, and exactly that many", db, 7, want, ""},
		{"more than there are", db, 20, want, ""},
		{"the default limit", db, 0, want, ""},
		{"past the biggest page", db, MaxLimit + 100, want, ""},
		{"pages smaller than asked", &smallPages{db: db}, 5, want[:5], want[4]},
		{"pages smaller than asked, to the end", &smallPages{db: db}, 20, want, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			list, next, err := ListAll(ctx, c.l, Filter{Limit: c.limit})
			if err != nil {
				t.Fatal(err)
			}
			if got := ids(list); !slices.Equal(got, c.want) {
				t.Errorf("runs\n%v\nwant\n%v", got, c.want)
			}
			if next != c.wantNext {
				t.Errorf("next = %q, want %q", next, c.wantNext)
			}
		})
	}

	// The filter carries through every page.
	list, _, err := ListAll(ctx, &smallPages{db: db}, Filter{Limit: 20, Before: want[2]})
	if err != nil || !slices.Equal(ids(list), want[3:]) {
		t.Errorf("Before = %v, %v", ids(list), err)
	}
	if list, next, err := ListAll(ctx, db, Filter{Factory: "nobody", Limit: 5}); err != nil || list == nil || len(list) != 0 || next != "" {
		t.Errorf("nothing found = %v, %q, %v", list, next, err)
	}
	if _, _, err := ListAll(ctx, db, Filter{Outcomes: []string{"done"}, Limit: 5}); err == nil {
		t.Error("a bad outcome should be refused")
	}
}

// brokenPages answers with a next it never gets to the end of.
type brokenPages struct{ calls int }

func (b *brokenPages) List(context.Context, Filter) ([]Summary, string, error) {
	b.calls++
	if b.calls > 3 {
		return nil, "", errors.New("asked too many times")
	}
	return nil, "run_loop", nil
}

func TestListAllStopsOnEmptyPages(t *testing.T) {
	b := &brokenPages{}
	list, next, err := ListAll(context.Background(), b, Filter{Limit: 10})
	if err != nil || len(list) != 0 || next != "" || b.calls != 1 {
		t.Errorf("ListAll = %v, %q, %v after %d calls", list, next, err, b.calls)
	}
}
