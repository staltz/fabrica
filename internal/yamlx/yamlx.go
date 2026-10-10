// Package yamlx reads YAML the way Fabrica wants: strictly.
//
// Fabrica treats unknown keys as errors. If you write `alwaysapply: true`
// instead of `alwaysApply: true`, a lenient reader would quietly ignore your
// typo and your rule would never apply. A strict reader stops and tells you,
// which is the whole point.
//
// It also knows how to split YAML frontmatter off the top of a Markdown file,
// which is how routines, rules and skills carry their settings.
package yamlx

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"
)

// DecodeStrict decodes one YAML document into v, failing on unknown keys and
// on files that hold more than one document. An empty file decodes to the
// zero value without complaint.
func DecodeStrict(data []byte, v any) error {
	err := decodeStrict(data, v)
	if err != nil && strings.Contains(err.Error(), "did not find expected ',' or ']'") {
		// The YAML library treats a `?` inside [ ... ] as special, so the
		// list syntax Fabrica uses for optional items, like
		// `connectors: [github, slack?]`, trips it up. Quote those items and
		// try once more.
		if fixed := quoteOptionalItems(data); !bytes.Equal(fixed, data) {
			// Whatever the retry says is the real problem (or none at all).
			return decodeStrict(fixed, v)
		}
	}
	return err
}

// optionalItemRe finds bare `name?` items inside a [ ... ] list.
var optionalItemRe = regexp.MustCompile(`([\[,]\s*)([A-Za-z0-9_.\-/]+\?)(\s*[,\]])`)

// quoteOptionalItems turns `[github, slack?]` into `[github, "slack?"]`.
func quoteOptionalItems(data []byte) []byte {
	for {
		next := optionalItemRe.ReplaceAll(data, []byte(`$1"$2"$3`))
		if bytes.Equal(next, data) {
			return next
		}
		data = next
	}
}

func decodeStrict(data []byte, v any) error {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(v); err != nil {
		if errors.Is(err, io.EOF) {
			return nil // empty file
		}
		return friendly(err)
	}
	// Anything after the first document is a mistake: Fabrica files hold
	// exactly one document.
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		if err != nil {
			return friendly(err)
		}
		return errors.New("this file has more than one YAML document (a second `---`); Fabrica expects exactly one")
	}
	return nil
}

// DecodeLenient decodes YAML but ignores unknown keys. We only use it for
// formats Fabrica doesn't own, like the frontmatter of standard Agent Skills,
// where other tools are allowed to add their own fields.
func DecodeLenient(data []byte, v any) error {
	if err := yaml.Unmarshal(data, v); err != nil {
		return friendly(err)
	}
	return nil
}

// Marshal writes v as YAML with two-space indentation.
func Marshal(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

var (
	unknownFieldRe  = regexp.MustCompile(`field (\S+) not found in type \S+`)
	cannotUnmarshal = regexp.MustCompile("cannot unmarshal !!(\\w+)(?: `([^`]*)`)? into (\\S+)")
)

// friendly rewrites the YAML library's error messages into something a
// person can act on. For example "field foo not found in type
// factory.manifestFile" becomes `unknown key "foo"`.
func friendly(err error) error {
	msg := err.Error()
	msg = strings.TrimPrefix(msg, "yaml: ")
	msg = strings.ReplaceAll(msg, "unmarshal errors:\n  ", "")
	msg = unknownFieldRe.ReplaceAllString(msg, `unknown key "$1" (check the spelling; Fabrica doesn't allow keys it doesn't know)`)
	msg = cannotUnmarshal.ReplaceAllStringFunc(msg, func(m string) string {
		parts := cannotUnmarshal.FindStringSubmatch(m)
		found := yamlTypeName(parts[1])
		if parts[2] != "" {
			found += fmt.Sprintf(" %q", parts[2])
		}
		return fmt.Sprintf("expected %s but found %s", goTypeName(parts[3]), found)
	})
	return errors.New(msg)
}

func yamlTypeName(tag string) string {
	switch tag {
	case "str":
		return "the text"
	case "seq":
		return "a list"
	case "map":
		return "a map"
	case "bool":
		return "the boolean"
	case "int", "float":
		return "the number"
	default:
		return tag
	}
}

func goTypeName(t string) string {
	switch {
	case strings.HasPrefix(t, "[]"):
		return "a list"
	case strings.HasPrefix(t, "map["):
		return "a map"
	case t == "string":
		return "text"
	case t == "bool":
		return "true or false"
	case strings.HasPrefix(t, "int") || strings.HasPrefix(t, "float"):
		return "a number"
	default:
		// A struct type, like factory.triggerFile.
		return "a map"
	}
}

// Frontmatter is a Markdown file split into its YAML header and its body.
type Frontmatter struct {
	// YAML is the text between the opening and closing `---` lines.
	YAML []byte
	// Body is everything after the closing `---` line.
	Body string
	// Present reports whether the file had frontmatter at all.
	Present bool
}

// SplitFrontmatter splits a Markdown file into frontmatter and body.
//
// Frontmatter must start on the very first line with `---` and end with
// another `---` line. A file that doesn't start with `---` has no
// frontmatter, and the whole file is the body.
func SplitFrontmatter(data []byte) (Frontmatter, error) {
	text := string(bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))) // drop a UTF-8 BOM
	text = strings.ReplaceAll(text, "\r\n", "\n")
	if !strings.HasPrefix(text, "---\n") && text != "---" {
		return Frontmatter{Body: text}, nil
	}
	rest := strings.TrimPrefix(text, "---")
	rest = strings.TrimPrefix(rest, "\n")
	// The closing line can be `---` at the very start of rest (empty
	// frontmatter) or `\n---` later on.
	var yamlPart, body string
	switch {
	case strings.HasPrefix(rest, "---\n") || rest == "---":
		yamlPart, body = "", strings.TrimPrefix(strings.TrimPrefix(rest, "---"), "\n")
	default:
		idx := strings.Index(rest, "\n---\n")
		if idx < 0 {
			if strings.HasSuffix(rest, "\n---") {
				idx = len(rest) - len("\n---")
				yamlPart, body = rest[:idx], ""
				break
			}
			return Frontmatter{}, errors.New("frontmatter starts with `---` but never ends; add a closing `---` line")
		}
		yamlPart, body = rest[:idx], rest[idx+len("\n---\n"):]
	}
	return Frontmatter{YAML: []byte(yamlPart), Body: body, Present: true}, nil
}
