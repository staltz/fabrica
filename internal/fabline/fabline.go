// Package fabline holds the Fabrica line: the one sentence every product
// repo's root AGENTS.md (and root CLAUDE.md, if there is one) must start
// with.
//
// AGENTS.md is the one file that Codex, Cursor and Claude Code all load on
// their own. Putting this sentence at the very top is how blueprint rules
// become always-on in every tool, without Fabrica ever writing files into
// your product repo.
package fabline

import (
	"bytes"
	"strings"
)

// Sentence is the exact Fabrica line, as of this version of Fabrica.
const Sentence = "Before starting, load this repo's working rules from the Fabrica MCP server; if you don't have it, ignore this line."

// accepted lists every sentence Fabrica has ever used, newest first. If a
// future version changes the wording, the old sentences stay in this list
// forever, so repos that were converted earlier never break on upgrade.
var accepted = []string{
	Sentence,
}

// Files lists the root files that must start with the line. CLAUDE.md only
// needs it when it exists, because Claude Code ignores AGENTS.md whenever
// a CLAUDE.md is present.
var Files = []string{"AGENTS.md", "CLAUDE.md"}

// StartsWithLine reports whether a file's first line is the Fabrica line.
// We're strict about it: the sentence must be the whole first line, exactly
// as written (a byte-order mark and trailing spaces are fine).
func StartsWithLine(content []byte) bool {
	content = bytes.TrimPrefix(content, []byte("\xef\xbb\xbf"))
	first, _, _ := strings.Cut(string(content), "\n")
	first = strings.TrimRight(first, " \t\r")
	for _, s := range accepted {
		if first == s {
			return true
		}
	}
	return false
}
