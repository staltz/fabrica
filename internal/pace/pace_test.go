package pace

import (
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	good := map[string]Pace{"5m": 5, "30m": 30, "90m": 90, "1h": 60, "4h": 240, "2d": 2 * 1440, "365d": 365 * 1440}
	for s, want := range good {
		got, err := Parse(s)
		if err != nil || got != want {
			t.Errorf("Parse(%q) = %d, %v; want %d", s, got, err, want)
		}
	}
	for _, s := range []string{"", "0m", "5", "m", "5s", "1w", "1.5h", "-5m", "05m", "5 m", "5M", "366d", "99999999999999999999m"} {
		if _, err := Parse(s); err == nil {
			t.Errorf("Parse(%q) should fail", s)
		}
	}
}

func TestString(t *testing.T) {
	cases := map[Pace]string{5: "5m", 60: "1h", 90: "90m", 120: "2h", 1440: "1d", 2880: "2d", 1500: "25h"}
	for p, want := range cases {
		if got := p.String(); got != want {
			t.Errorf("%d.String() = %q, want %q", int(p), got, want)
		}
	}
}

func TestCron(t *testing.T) {
	cases := map[int]string{
		1:                 "* * * * *",
		5:                 "*/5 * * * *",
		30:                "*/30 * * * *",
		60:                "0 * * * *",
		120:               "0 */2 * * *",
		12 * 60:           "0 */12 * * *",
		1440:              "0 0 * * *",
		2 * 1440:          "0 0 */2 * *",
		7 * 1440:          "0 0 * * 1",
		31 * 1440:         "0 0 */31 * *",
		Pace(30).Times(4): "0 */2 * * *",
	}
	for minutes, want := range cases {
		got, err := Cron(minutes)
		if err != nil || got != want {
			t.Errorf("Cron(%d) = %q, %v; want %q", minutes, got, err, want)
		}
	}
	bad := map[int]string{
		7:         "every 6m or every 10m",
		90:        "every 1h or every 2h",
		5 * 60:    "every 4h or every 6h",
		25 * 60:   "every 1d or every 2d",
		32 * 1440: "every 31d",
		0:         "positive",
	}
	for minutes, want := range bad {
		_, err := Cron(minutes)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Cron(%d) error = %v, want one mentioning %q", minutes, err, want)
		}
	}
}
