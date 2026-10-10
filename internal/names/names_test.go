package names

import "testing"

func TestValidate(t *testing.T) {
	for _, ok := range []string{"pr-review", "a", "tests-first", "v2"} {
		if err := Validate(ok); err != nil {
			t.Errorf("Validate(%q) = %v", ok, err)
		}
	}
	for _, bad := range []string{"", "PR", "pr_review", "-x", "x-", "a--b", "has space"} {
		if err := Validate(bad); err == nil {
			t.Errorf("Validate(%q) should fail", bad)
		}
	}
}

func TestParseRef(t *testing.T) {
	r, err := ParseRef("skills/pr-review")
	if err != nil || r.Kind != Skills || r.Name != "pr-review" || r.String() != "skills/pr-review" {
		t.Errorf("ParseRef = %+v, %v", r, err)
	}
	for _, bad := range []string{"pr-review", "widgets/x", "skills/", "skills/a/b"} {
		if _, err := ParseRef(bad); err == nil {
			t.Errorf("ParseRef(%q) should fail", bad)
		}
	}
}

func TestParseMaybeNames(t *testing.T) {
	got, err := ParseMaybeNames([]string{"github", "slack?"})
	if err != nil || len(got) != 2 || got[0].Optional || !got[1].Optional || got[1].Name != "slack" {
		t.Errorf("ParseMaybeNames = %+v, %v", got, err)
	}
	if _, err := ParseMaybeNames([]string{"github", "github?"}); err == nil {
		t.Error("duplicates should fail")
	}
}
