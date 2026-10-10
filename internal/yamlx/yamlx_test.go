package yamlx

import (
	"strings"
	"testing"
)

type rule struct {
	Description string `yaml:"description"`
	AlwaysApply bool   `yaml:"alwaysApply"`
}

func TestDecodeStrictRejectsUnknownKeys(t *testing.T) {
	var r rule
	err := DecodeStrict([]byte("description: x\nalwaysapply: true\n"), &r)
	if err == nil || !strings.Contains(err.Error(), `unknown key "alwaysapply"`) {
		t.Fatalf("want a friendly unknown-key error, got %v", err)
	}
}

func TestDecodeStrictWrongType(t *testing.T) {
	var r rule
	err := DecodeStrict([]byte("alwaysApply: [1, 2]\n"), &r)
	if err == nil || !strings.Contains(err.Error(), "expected true or false") {
		t.Fatalf("want a friendly type error, got %v", err)
	}
}

func TestDecodeStrictEmptyAndMultiDoc(t *testing.T) {
	var r rule
	if err := DecodeStrict(nil, &r); err != nil {
		t.Errorf("empty input should decode fine, got %v", err)
	}
	if err := DecodeStrict([]byte("description: a\n---\ndescription: b\n"), &r); err == nil {
		t.Error("two documents should be an error")
	}
}

func TestSplitFrontmatter(t *testing.T) {
	cases := []struct {
		in, yaml, body string
		present        bool
	}{
		{"---\ntrigger: x\n---\nHello\n", "trigger: x", "Hello\n", true},
		{"---\r\ntrigger: x\r\n---\r\nHello", "trigger: x", "Hello", true},
		{"---\n---\nBody", "", "Body", true},
		{"No frontmatter here\n", "", "No frontmatter here\n", false},
		{"---\nonly: yaml\n---", "only: yaml", "", true},
		{"\xef\xbb\xbf---\na: 1\n---\nb", "a: 1", "b", true},
	}
	for _, c := range cases {
		fm, err := SplitFrontmatter([]byte(c.in))
		if err != nil {
			t.Errorf("SplitFrontmatter(%q): %v", c.in, err)
			continue
		}
		if string(fm.YAML) != c.yaml || fm.Body != c.body || fm.Present != c.present {
			t.Errorf("SplitFrontmatter(%q) = %q, %q, %v; want %q, %q, %v", c.in, fm.YAML, fm.Body, fm.Present, c.yaml, c.body, c.present)
		}
	}
	if _, err := SplitFrontmatter([]byte("---\nnever: ends\n")); err == nil {
		t.Error("unterminated frontmatter should be an error")
	}
}

func TestDecodeStrictOptionalFlowItems(t *testing.T) {
	var v struct {
		Connectors []string `yaml:"connectors"`
		Expects    []string `yaml:"expects"`
	}
	err := DecodeStrict([]byte("connectors: [github, slack?]\nexpects: [test?, deploy?]\n"), &v)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(v.Connectors, ",") != "github,slack?" || strings.Join(v.Expects, ",") != "test?,deploy?" {
		t.Errorf("got %+v", v)
	}
}
