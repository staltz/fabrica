// Package diag is how Fabrica reports problems.
//
// Almost every check in Fabrica produces a list of diagnostics instead of
// stopping at the first problem. That way `fabrica check` can show you
// everything that's wrong in one go, instead of making you fix one thing,
// re-run, fix the next thing, and so on.
//
// A diagnostic is either an error (Fabrica refuses to continue), a warning
// (worth knowing, never blocks anything) or a note (plain information).
package diag

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Severity says how serious a diagnostic is.
type Severity int

const (
	// Error means something is wrong and Fabrica won't continue until it's fixed.
	Error Severity = iota
	// Warning means something looks off, but it never stops anything.
	Warning
	// Note is plain information, like "this deployment follows a branch".
	Note
)

func (s Severity) String() string {
	switch s {
	case Error:
		return "error"
	case Warning:
		return "warning"
	default:
		return "note"
	}
}

// Symbol is the little marker we print in front of a diagnostic.
func (s Severity) Symbol() string {
	switch s {
	case Error:
		return "✗"
	case Warning:
		return "!"
	default:
		return "·"
	}
}

// Diagnostic is one problem (or one piece of information) that Fabrica found.
type Diagnostic struct {
	Severity Severity `json:"severity"`
	// Code is a short, stable identifier like "import.collision". Tests and
	// scripts can match on it without depending on the exact wording.
	Code string `json:"code"`
	// Where points at the thing the diagnostic is about: a file, an item like
	// "skills/pr-review", or a repo like "github.com/acme/app".
	Where string `json:"where,omitempty"`
	// Message says what's wrong, in plain English.
	Message string `json:"message"`
	// Hint says how to fix it, when we know.
	Hint string `json:"hint,omitempty"`
}

// String renders the diagnostic on one or two lines, like:
//
//	✗ routines/pr-review.md: skill "pr-review" doesn't exist in this factory
//	  hint: add skills/pr-review to the import's only: list
func (d Diagnostic) String() string {
	var b strings.Builder
	b.WriteString(d.Severity.Symbol())
	b.WriteString(" ")
	if d.Where != "" {
		b.WriteString(d.Where)
		b.WriteString(": ")
	}
	b.WriteString(d.Message)
	if d.Hint != "" {
		b.WriteString("\n  hint: ")
		b.WriteString(d.Hint)
	}
	return b.String()
}

// List is a bunch of diagnostics. The zero value is ready to use.
type List []Diagnostic

// Add appends diagnostics to the list.
func (l *List) Add(ds ...Diagnostic) { *l = append(*l, ds...) }

// Errorf records an error.
func (l *List) Errorf(code, where, format string, args ...any) *Diagnostic {
	return l.add(Error, code, where, format, args...)
}

// Warnf records a warning.
func (l *List) Warnf(code, where, format string, args ...any) *Diagnostic {
	return l.add(Warning, code, where, format, args...)
}

// Notef records a note.
func (l *List) Notef(code, where, format string, args ...any) *Diagnostic {
	return l.add(Note, code, where, format, args...)
}

// add appends a diagnostic and returns a pointer to it, so callers can chain
// Hintf. Use the pointer right away: the next append may move the list's
// backing array, and an old pointer would then point at a stale copy.
func (l *List) add(sev Severity, code, where, format string, args ...any) *Diagnostic {
	*l = append(*l, Diagnostic{
		Severity: sev,
		Code:     code,
		Where:    where,
		Message:  fmt.Sprintf(format, args...),
	})
	return &(*l)[len(*l)-1]
}

// Hintf sets the fix-it hint on a diagnostic. It returns the same diagnostic
// so you can write list.Errorf(...).Hintf("...") in one go.
func (d *Diagnostic) Hintf(format string, args ...any) *Diagnostic {
	d.Hint = fmt.Sprintf(format, args...)
	return d
}

// HasErrors reports whether any diagnostic in the list is an error.
func (l List) HasErrors() bool {
	for _, d := range l {
		if d.Severity == Error {
			return true
		}
	}
	return false
}

// Only returns the diagnostics with the given severity.
func (l List) Only(sev Severity) List {
	var out List
	for _, d := range l {
		if d.Severity == sev {
			out = append(out, d)
		}
	}
	return out
}

// HasCode reports whether any diagnostic has the given code. Handy in tests.
func (l List) HasCode(code string) bool {
	for _, d := range l {
		if d.Code == code {
			return true
		}
	}
	return false
}

// Prefixed returns a copy of the list where every Where is prefixed, which is
// how we say "this problem is inside import `base`" or "inside deployment
// `pr-review`".
func (l List) Prefixed(prefix string) List {
	out := make(List, len(l))
	for i, d := range l {
		if d.Where == "" {
			d.Where = prefix
		} else {
			d.Where = prefix + " › " + d.Where
		}
		out[i] = d
	}
	return out
}

// Sorted returns the list ordered by severity first (errors on top), then by
// where, then by message. Sorting makes output stable, which keeps tests and
// CI logs easy to read.
func (l List) Sorted() List {
	out := append(List(nil), l...)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Severity != b.Severity {
			return a.Severity < b.Severity
		}
		if a.Where != b.Where {
			return a.Where < b.Where
		}
		return a.Message < b.Message
	})
	return out
}

// Dedup drops exact duplicates while keeping the original order.
func (l List) Dedup() List {
	seen := map[Diagnostic]bool{}
	var out List
	for _, d := range l {
		if !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	return out
}

// String renders all diagnostics, one per line.
func (l List) String() string {
	var b strings.Builder
	for i, d := range l {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString(d.String())
	}
	return b.String()
}

// Err turns the errors in the list into a Go error, or returns nil when there
// are none. Warnings and notes are left out.
func (l List) Err() error {
	errs := l.Only(Error)
	if len(errs) == 0 {
		return nil
	}
	return &Failure{List: errs}
}

// Failure is a Go error that carries a list of error diagnostics.
type Failure struct {
	List List
}

func (f *Failure) Error() string {
	if len(f.List) == 1 {
		return f.List[0].Message
	}
	return fmt.Sprintf("%d problems found:\n%s", len(f.List), f.List.String())
}

// AsList pulls the diagnostics out of an error made by List.Err. Any other
// error becomes a single diagnostic with the given code.
func AsList(err error, code, where string) List {
	if err == nil {
		return nil
	}
	var f *Failure
	if errors.As(err, &f) {
		return f.List
	}
	return List{{Severity: Error, Code: code, Where: where, Message: err.Error()}}
}
