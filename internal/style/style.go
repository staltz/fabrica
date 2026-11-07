// Package style colors CLI output, but only when a person is looking at
// it: when the output is a terminal, NO_COLOR isn't set and TERM isn't
// "dumb". FORCE_COLOR turns it on anyway. Pipes, files and tests get plain
// text, byte for byte the same as without this package.
package style

import (
	"io"
	"os"
	"strings"

	"github.com/staltz/fabrica/internal/diag"
)

// Style colors text, or leaves it alone when it's off. The zero value is
// off.
type Style struct{ on bool }

// For returns the style for output going to w.
func For(w io.Writer) Style {
	if v := os.Getenv("FORCE_COLOR"); v != "" && v != "0" {
		return Style{on: true}
	}
	if os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return Style{}
	}
	f, ok := w.(*os.File)
	if !ok {
		return Style{}
	}
	info, err := f.Stat()
	return Style{on: err == nil && info.Mode()&os.ModeCharDevice != 0}
}

// On reports whether this style colors anything.
func (s Style) On() bool { return s.on }

func (s Style) wrap(code, text string) string {
	if !s.on || text == "" {
		return text
	}
	return "\x1b[" + code + "m" + text + "\x1b[0m"
}

func (s Style) Bold(text string) string   { return s.wrap("1", text) }
func (s Style) Dim(text string) string    { return s.wrap("2", text) }
func (s Style) Red(text string) string    { return s.wrap("31", text) }
func (s Style) Green(text string) string  { return s.wrap("32", text) }
func (s Style) Yellow(text string) string { return s.wrap("33", text) }
func (s Style) Blue(text string) string   { return s.wrap("34", text) }
func (s Style) Cyan(text string) string   { return s.wrap("36", text) }

// Severity colors text the way its severity reads: errors red, warnings
// yellow, notes dim.
func (s Style) Severity(sev diag.Severity, text string) string {
	switch sev {
	case diag.Error:
		return s.Red(text)
	case diag.Warning:
		return s.Yellow(text)
	}
	return s.Dim(text)
}

// Diag renders a diagnostic like diag.Diagnostic.String does, with its
// symbol colored by severity and its hint dimmed.
func (s Style) Diag(d diag.Diagnostic) string {
	if !s.on {
		return d.String()
	}
	var b strings.Builder
	b.WriteString(s.Severity(d.Severity, d.Severity.Symbol()))
	b.WriteString(" ")
	if d.Where != "" {
		b.WriteString(s.Bold(d.Where + ":"))
		b.WriteString(" ")
	}
	b.WriteString(d.Message)
	if d.Hint != "" {
		b.WriteString("\n  ")
		b.WriteString(s.Dim("hint: " + d.Hint))
	}
	return b.String()
}

// Mark colors a line that starts with a change symbol: + green, ~ yellow,
// - red, = and ! cyan. Only the symbol and the word after it are colored.
// Anything else comes back as it was.
func (s Style) Mark(line string) string {
	if !s.on || len(line) < 2 || line[1] != ' ' {
		return line
	}
	color := map[byte]func(string) string{'+': s.Green, '~': s.Yellow, '-': s.Red, '=': s.Cyan, '!': s.Cyan}[line[0]]
	if color == nil {
		return line
	}
	end := 2 // the symbol, then the word
	for end < len(line) && line[end] != ' ' {
		end++
	}
	return color(line[:end]) + line[end:]
}
