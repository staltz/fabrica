package fabline

import "testing"

func TestStartsWithLine(t *testing.T) {
	for in, want := range map[string]bool{
		Sentence + "\n\n# My app\n":         true,
		Sentence:                            true,
		"\xef\xbb\xbf" + Sentence + " \r\n": true,
		"# My app\n" + Sentence + "\n":      false,
		"> " + Sentence + "\n":              false,
		"":                                  false,
	} {
		if got := StartsWithLine([]byte(in)); got != want {
			t.Errorf("StartsWithLine(%q) = %v, want %v", in, got, want)
		}
	}
}
