package runs

import "context"

// Lister reads one page of runs, the way List does. A *DB is one, and so is
// the admin API's client, which lets the CLI read local and remote history
// with the same code.
type Lister interface {
	List(ctx context.Context, f Filter) (runs []Summary, next string, err error)
}

// ListAll reads runs newest first, page after page, until it has f.Limit of
// them (DefaultLimit when it's zero) or there are no more. Unlike List, the
// limit can be more than MaxLimit. next is the Before value that carries on
// from where it stopped, and it's empty when it read every run there is.
func ListAll(ctx context.Context, l Lister, f Filter) (list []Summary, next string, err error) {
	want := f.Limit
	if want == 0 {
		want = DefaultLimit
	}
	list = []Summary{}
	for len(list) < want {
		page := f
		page.Limit = min(want-len(list), MaxLimit)
		got, more, err := l.List(ctx, page)
		if err != nil {
			return nil, "", err
		}
		list = append(list, got...)
		next = more
		if len(got) == 0 {
			// A page with nothing in it can't be followed by another.
			next = ""
		}
		if next == "" {
			break
		}
		f.Before = next
	}
	return list, next, nil
}
