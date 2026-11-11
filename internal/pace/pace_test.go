package pace

import (
	"testing"
	"time"
)

func TestParse(t *testing.T) {
	good := map[string]Pace{"5m": 5, "7m": 7, "30m": 30, "90m": 90, "1h": 60, "4h": 240, "2d": 2 * 1440, "365d": 365 * 1440}
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

func TestBeats(t *testing.T) {
	at := func(s string) time.Time {
		v, err := time.Parse(time.RFC3339, s)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}

	// Beats start on the epoch's grid, whatever the time zone of the clock.
	p := Pace(30)
	b := p.BeatAt(at("2026-11-10T14:47:12+02:00"))
	if want := at("2026-11-10T12:30:00Z"); !b.At.Equal(want) || b.At.Location() != time.UTC {
		t.Errorf("BeatAt = %s, want %s", b.At, want)
	}
	if b != p.Beat(b.N) {
		t.Errorf("Beat(%d) = %+v, want %+v", b.N, p.Beat(b.N), b)
	}
	if next := p.Beat(b.N + 1); !next.At.Equal(b.At.Add(30 * time.Minute)) {
		t.Errorf("the next beat starts at %s", next.At)
	}

	// every: 4 at 30m fires every 2h, on the hour from midnight UTC.
	if !p.BeatAt(at("2026-11-10T12:00:00Z")).Fires(4) || p.BeatAt(at("2026-11-10T12:30:00Z")).Fires(4) || p.BeatAt(at("2026-11-10T13:00:00Z")).Fires(4) {
		t.Error("every 4 beats of 30m should fire at 12:00 and not at 12:30 or 13:00")
	}
	if !p.BeatAt(at("2026-11-10T14:59:59Z")).Fires(1) {
		t.Error("every beat fires on every 1")
	}

	// Paces cron can't repeat are fine: 7m beats every 7 minutes.
	odd := Pace(7)
	first := odd.BeatAt(at("2026-11-10T12:00:00Z"))
	if got := odd.Beat(first.N + 1).At.Sub(first.At); got != 7*time.Minute {
		t.Errorf("7m beats are %s apart", got)
	}
	if (Beat{N: 8}).Fires(0) {
		t.Error("every: 0 never fires")
	}
}
