package mcpserver

import (
	"strings"
	"testing"

	"github.com/staltz/fabrica/internal/blueprint"
	"github.com/staltz/fabrica/internal/names"
)

func TestNestHeadings(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "plain prose",
			in:   "Review the diff.\n\n1. Read it.",
			want: "Review the diff.\n\n1. Read it.",
		},
		{
			name: "h1 and h2 under a level-2 parent",
			in:   "# Do this\n\nBody.\n\n## Steps\n\n1. Go.",
			want: "### Do this\n\nBody.\n\n#### Steps\n\n1. Go.",
		},
		{
			name: "shallowest is already h2",
			in:   "## Steps\n\n### Detail",
			want: "### Steps\n\n#### Detail",
		},
		{
			name: "already deeper than the parent",
			in:   "### Already under\n\n#### Deeper",
			want: "### Already under\n\n#### Deeper",
		},
		{
			name: "fenced code is left alone",
			in:   "```md\n# not a heading\n```\n\n# Real",
			want: "```md\n# not a heading\n```\n\n### Real",
		},
		{
			name: "a longer fence holds a shorter one",
			in:   "````\n```\n# hi\n```\n````\n\n# Real",
			want: "````\n```\n# hi\n```\n````\n\n### Real",
		},
		{
			name: "tilde fence",
			in:   "~~~\n## code\n~~~\n\n# Title",
			want: "~~~\n## code\n~~~\n\n### Title",
		},
		{
			name: "hash without a space is not a heading",
			in:   "#not-a-heading\n\n# Yes",
			want: "#not-a-heading\n\n### Yes",
		},
		{
			name: "indented heading and a closing sequence",
			in:   "  ## Indented ##",
			want: "  ### Indented ##",
		},
		{
			name: "level 6 is the bottom",
			in:   "# A\n\n###### F",
			want: "### A\n\n###### F",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := nestHeadings(tc.in, 2); got != tc.want {
				t.Errorf("nestHeadings =\n%s\nwant\n%s", got, tc.want)
			}
		})
	}
}

func TestRoutineTextNestsPromptHeadings(t *testing.T) {
	r := &blueprint.Routine{
		ItemInfo: blueprint.ItemInfo{Ref: names.Ref{Name: "check"}},
		Trigger:  blueprint.Trigger{On: "schedule", Cron: "0 9 * * 1"},
		Prompt:   "# Check deps\n\n## How\n\nLook.\n\n```\n# comment\n```",
	}
	snap := &Snapshot{Blueprint: &blueprint.Blueprint{Source: blueprint.Source{Dir: "bp"}}}
	got := routineText(snap, r, nil, "github.com/acme/app", "", nil)
	for _, want := range []string{"## Instructions\n\n### Check deps\n\n#### How\n", "```\n# comment\n```", "## Working rules"} {
		if !strings.Contains(got, want) {
			t.Errorf("routine text is missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "\n# Check deps\n") || strings.Contains(got, "\n## How\n") {
		t.Errorf("prompt headings should sit under Instructions:\n%s", got)
	}
}
