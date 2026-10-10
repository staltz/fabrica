// Package promptblock wraps text that someone should copy and paste to an
// AI agent (a prompt, or a line for AGENTS.md) between start and end
// delimiters, so it's clear exactly where the text begins and ends.
package promptblock

import "strings"

// Begin and End are the delimiter lines around a prompt.
const (
	Begin = "-----BEGIN PROMPT-----"
	End   = "-----END PROMPT-----"
)

// Wrap puts text between the delimiters, one per line. Nothing is
// indented, so copying the lines between the delimiters gives the text
// exactly, even where leading spaces would matter (like the first line of
// AGENTS.md).
func Wrap(text string) string {
	return Begin + "\n" + strings.TrimSpace(text) + "\n" + End
}
