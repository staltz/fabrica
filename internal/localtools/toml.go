package localtools

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Codex keeps its MCP servers in ~/.codex/config.toml, as tables like:
//
//	[mcp_servers.fabrica]
//	command = "/usr/local/bin/fabrica"
//	args = ["mcp", "--runtime", "codex-cli"]
//
// Rewriting the whole file through a TOML library would lose your comments
// and formatting, so instead we edit it as text: we find Fabrica's own
// tables (any [mcp_servers.fabrica…] table), cut them out, and append the
// ones we want at the end. Every other line stays exactly as it was.

var tomlHeaderRe = regexp.MustCompile(`^\s*\[\[?\s*([^\[\]]+?)\s*\]\]?\s*(#.*)?$`)

const codexStartupTimeout = 60

// tomlTable is one table found in the file: its header name and the lines
// from its header up to (not including) the next header.
type tomlTable struct {
	name  string // normalized, like mcp_servers.fabrica
	start int    // first line (the header)
	end   int    // one past the last line
}

func scanTables(lines []string) []tomlTable {
	var tables []tomlTable
	for i, line := range lines {
		m := tomlHeaderRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		if n := len(tables); n > 0 {
			tables[n-1].end = i
		}
		tables = append(tables, tomlTable{name: normalizeTableName(m[1]), start: i, end: len(lines)})
	}
	return tables
}

// normalizeTableName turns `mcp_servers . "fabrica"` into mcp_servers.fabrica.
func normalizeTableName(s string) string {
	parts := strings.Split(s, ".")
	for i, p := range parts {
		p = strings.TrimSpace(p)
		if unq, err := strconv.Unquote(p); err == nil {
			p = unq
		} else {
			p = strings.Trim(p, "'")
		}
		parts[i] = p
	}
	return strings.Join(parts, ".")
}

// fabricaServer returns the server name if the table belongs to a Fabrica
// MCP server (mcp_servers.fabrica, mcp_servers.fabrica-x, or a sub-table
// like mcp_servers.fabrica.env).
func fabricaServer(table string) (string, bool) {
	rest, ok := strings.CutPrefix(table, "mcp_servers.")
	if !ok {
		return "", false
	}
	name, _, _ := strings.Cut(rest, ".")
	return name, IsFabricaEntry(name)
}

func readCodexServers(data []byte) ([]MCPEntry, error) {
	lines := splitLines(string(data))
	byName := map[string]*MCPEntry{}
	var order []string
	for _, t := range scanTables(lines) {
		name, ours := fabricaServer(t.name)
		if !ours || t.name != "mcp_servers."+name {
			continue
		}
		e := &MCPEntry{Name: name, Transport: "stdio"}
		for _, line := range lines[t.start+1 : t.end] {
			key, value, ok := strings.Cut(stripComment(line), "=")
			if !ok {
				continue
			}
			key, value = strings.TrimSpace(key), strings.TrimSpace(value)
			var err error
			switch key {
			case "command":
				e.Command, err = parseTOMLString(value)
			case "args":
				e.Args, err = parseTOMLStrings(value)
			case "env_vars":
				e.EnvNames, err = parseTOMLStrings(value)
			case "url":
				e.URL, err = parseTOMLString(value)
				e.Transport = "http"
			case "bearer_token_env_var":
				e.BearerEnv, err = parseTOMLString(value)
			}
			if err != nil {
				return nil, fmt.Errorf("[mcp_servers.%s] %s: %v", name, key, err)
			}
		}
		if _, seen := byName[name]; !seen {
			order = append(order, name)
		}
		byName[name] = e
	}
	var out []MCPEntry
	for _, name := range order {
		out = append(out, *byName[name])
	}
	return out, nil
}

func writeCodexServers(data []byte, entries []MCPEntry) []byte {
	lines := splitLines(string(data))
	drop := make([]bool, len(lines))
	for _, t := range scanTables(lines) {
		if _, ours := fabricaServer(t.name); ours {
			for i := t.start; i < t.end; i++ {
				drop[i] = true
			}
		}
	}
	var kept []string
	for i, line := range lines {
		if !drop[i] {
			kept = append(kept, line)
		}
	}
	// Trim blank lines left at the end, so repeated applies don't grow the
	// file.
	for len(kept) > 0 && strings.TrimSpace(kept[len(kept)-1]) == "" {
		kept = kept[:len(kept)-1]
	}
	sorted := append([]MCPEntry(nil), entries...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	for _, e := range sorted {
		if len(kept) > 0 {
			kept = append(kept, "")
		}
		kept = append(kept, "[mcp_servers."+e.Name+"]",
			"# Managed by `fabrica apply`; changes made here will be overwritten.")
		switch e.Transport {
		case "stdio":
			kept = append(kept, "command = "+tomlQuote(e.Command), "args = "+tomlArray(e.Args))
			if len(e.EnvNames) > 0 {
				kept = append(kept, "env_vars = "+tomlArray(e.EnvNames))
			}
		default:
			kept = append(kept, "url = "+tomlQuote(e.URL))
			if e.BearerEnv != "" {
				kept = append(kept, "bearer_token_env_var = "+tomlQuote(e.BearerEnv))
			}
		}
		if e.Name == "fabrica" {
			// The first session after an upgrade may need to fetch the
			// blueprint, which can take longer than Codex's 10-second default.
			kept = append(kept, fmt.Sprintf("startup_timeout_sec = %d", codexStartupTimeout))
		}
	}
	if len(kept) == 0 {
		return nil
	}
	return []byte(strings.Join(kept, "\n") + "\n")
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(strings.ReplaceAll(s, "\r\n", "\n"), "\n"), "\n")
}

// stripComment drops a trailing # comment that isn't inside a string.
func stripComment(line string) string {
	inStr := byte(0)
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case inStr != 0:
			if c == '\\' && inStr == '"' {
				i++
			} else if c == inStr {
				inStr = 0
			}
		case c == '"' || c == '\'':
			inStr = c
		case c == '#':
			return line[:i]
		}
	}
	return line
}

// tomlQuote writes a TOML basic string.
func tomlQuote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"':
			b.WriteString(`\"`)
		case r == '\\':
			b.WriteString(`\\`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\t':
			b.WriteString(`\t`)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, `\u%04X`, r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

func tomlArray(list []string) string {
	parts := make([]string, len(list))
	for i, s := range list {
		parts[i] = tomlQuote(s)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

// parseTOMLString reads a basic ("…") or literal ('…') TOML string.
func parseTOMLString(s string) (string, error) {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "'") && strings.HasSuffix(s, "'") && len(s) >= 2 {
		return s[1 : len(s)-1], nil
	}
	if !strings.HasPrefix(s, `"`) || !strings.HasSuffix(s, `"`) || len(s) < 2 {
		return "", fmt.Errorf("expected a string, got %s", s)
	}
	var b strings.Builder
	body := s[1 : len(s)-1]
	for i := 0; i < len(body); i++ {
		c := body[i]
		if c != '\\' {
			b.WriteByte(c)
			continue
		}
		i++
		if i >= len(body) {
			return "", fmt.Errorf("bad escape in %s", s)
		}
		switch body[i] {
		case '"', '\\':
			b.WriteByte(body[i])
		case 'n':
			b.WriteByte('\n')
		case 't':
			b.WriteByte('\t')
		case 'r':
			b.WriteByte('\r')
		case 'u', 'U':
			n := 4
			if body[i] == 'U' {
				n = 8
			}
			if i+1+n > len(body) {
				return "", fmt.Errorf("bad escape in %s", s)
			}
			code, err := strconv.ParseUint(body[i+1:i+1+n], 16, 32)
			if err != nil {
				return "", fmt.Errorf("bad escape in %s", s)
			}
			b.WriteRune(rune(code))
			i += n
		default:
			return "", fmt.Errorf("bad escape in %s", s)
		}
	}
	return b.String(), nil
}

// parseTOMLStrings reads a one-line array of strings, like ["a", "b"].
func parseTOMLStrings(s string) ([]string, error) {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "[") || !strings.HasSuffix(s, "]") {
		return nil, fmt.Errorf("expected an array of strings, got %s", s)
	}
	body := strings.TrimSpace(s[1 : len(s)-1])
	var out []string
	for body != "" {
		end, err := stringEnd(body)
		if err != nil {
			return nil, err
		}
		str, err := parseTOMLString(body[:end])
		if err != nil {
			return nil, err
		}
		out = append(out, str)
		body = strings.TrimSpace(body[end:])
		body = strings.TrimSpace(strings.TrimPrefix(body, ","))
	}
	return out, nil
}

// stringEnd finds where the string at the start of s ends.
func stringEnd(s string) (int, error) {
	if s == "" || s[0] != '"' && s[0] != '\'' {
		return 0, fmt.Errorf("expected a string in %s", s)
	}
	q := s[0]
	for i := 1; i < len(s); i++ {
		if q == '"' && s[i] == '\\' {
			i++
			continue
		}
		if s[i] == q {
			return i + 1, nil
		}
	}
	return 0, fmt.Errorf("unterminated string in %s", s)
}
