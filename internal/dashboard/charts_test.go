package dashboard

import (
	"fmt"
	"math"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/staltz/fabrica/internal/runs"
)

func TestNiceScale(t *testing.T) {
	for _, tc := range []struct {
		max       float64
		want      int
		step, top float64
	}{
		{0, 4, 1, 1}, {1, 4, 1, 1}, {3, 4, 1, 3}, {4, 4, 1, 4}, {5, 4, 2, 6}, {7, 4, 2, 8}, {9, 4, 5, 10}, {12, 4, 5, 15},
		{38, 4, 10, 40}, {100, 4, 50, 100}, {60, 4, 20, 60}, {1234, 4, 500, 1500},
	} {
		step, top := niceScale(tc.max, tc.want, 1)
		if step != tc.step || top != tc.top {
			t.Errorf("niceScale(%v, %d, 1) = %v, %v; want %v, %v", tc.max, tc.want, step, top, tc.step, tc.top)
		}
		if tc.max > 0 && top < tc.max {
			t.Errorf("niceScale(%v) tops out at %v", tc.max, top)
		}
	}
}

func TestCompact(t *testing.T) {
	for n, want := range map[float64]string{0: "0", 7: "7", 950: "950", 1000: "1K", 1500: "1.5K", 9999: "10K", 12000: "12K", 120000: "120K"} {
		if got := compact(n); got != want {
			t.Errorf("compact(%v) = %q, want %q", n, got, want)
		}
	}
}

func day0() time.Time { return time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC) }

func TestDaysBetweenFillsInEveryDay(t *testing.T) {
	counts := []runs.DayCounts{{Day: day0().AddDate(0, 0, 2), Runs: 3, Outcomes: map[string]int{"failed": 3}}}
	days := daysBetween(day0().Add(7*time.Hour), day0().AddDate(0, 0, 4).Add(5*time.Hour), counts)
	if len(days) != 5 {
		t.Fatalf("%d days, want 5", len(days))
	}
	for i, d := range days {
		if want := day0().AddDate(0, 0, i); !d.Day.Equal(want) {
			t.Errorf("day %d is %v", i, d.Day)
		}
		if (i == 2) != (d.Runs == 3) || d.Outcomes == nil {
			t.Errorf("day %d: %+v", i, d)
		}
	}
}

func buckets(spec ...map[string]int) []dayBucket {
	var out []dayBucket
	for i, o := range spec {
		b := dayBucket{Day: day0().AddDate(0, 0, i), Outcomes: map[string]int{}}
		for k, n := range o {
			b.Outcomes[k] = n
			b.Runs += n
		}
		out = append(out, b)
	}
	return out
}

var number = regexp.MustCompile(`-?\d+(\.\d+)?`)

// bounds finds the top and bottom of a bar's path, which starts at its
// bottom left and only ever moves by drawing, so its y values are what
// follow "M x y" plus the relative moves.
func barSpan(t *testing.T, d string) (top, bottom float64) {
	t.Helper()
	nums := number.FindAllString(d, -1)
	var y float64
	var parsed []float64
	for _, n := range nums {
		var f float64
		if _, err := fmt.Sscan(n, &f); err != nil {
			t.Fatalf("%q in %q", n, d)
		}
		parsed = append(parsed, f)
	}
	y = parsed[1]
	switch {
	case strings.Contains(d, "a"): // rounded: M x y+h, then v up by h-r, an arc, ...
		bottom = y
		var v float64
		v = parsed[2]
		top = y + v - 4 // the arc takes it the last r (4) up
	default: // M x y h w v h h -w z
		top, bottom = y, y+parsed[3]
	}
	return top, bottom
}

func TestBarChartGeometry(t *testing.T) {
	days := buckets(
		map[string]int{"succeeded": 3, "failed": 1},
		map[string]int{},
		map[string]int{"succeeded": 1, "in_progress": 1, "stale": 1, "failed": 1},
		map[string]int{"failed": 8},
	)
	c := newBarChart("c", days, "last 7 days")
	if len(c.Cols) != 4 || len(c.Ticks) < 2 {
		t.Fatalf("%d columns, %d ticks", len(c.Cols), len(c.Ticks))
	}
	// The ticks run from the baseline up, round, to at least the busiest day.
	if c.Ticks[0].Y != c.Baseline || c.Ticks[0].Label != "0" || c.Ticks[len(c.Ticks)-1].Label != "8" && c.Ticks[len(c.Ticks)-1].Label != "10" {
		t.Errorf("ticks %+v", c.Ticks)
	}
	// Every segment sits between the top of the plot and the baseline, one
	// per outcome that has runs, stacked in a fixed order, and only the top
	// of a stack is rounded.
	for i, col := range c.Cols {
		var want int
		for _, n := range days[i].Outcomes {
			if n > 0 {
				want++
			}
		}
		if len(col.Segs) != want {
			t.Errorf("column %d has %d segments, want %d", i, len(col.Segs), want)
		}
		for j, s := range col.Segs {
			if j > 0 && outcomeRank(s.Class) <= outcomeRank(col.Segs[j-1].Class) {
				t.Errorf("column %d stacks %s above %s", i, s.Class, col.Segs[j-1].Class)
			}
			if rounded := strings.Contains(s.D, "a"); rounded != (j == len(col.Segs)-1) {
				t.Errorf("column %d segment %d rounded = %v", i, j, rounded)
			}
			if (s.Pattern == "") != (s.Class == "o-succeeded") {
				t.Errorf("segment %s has pattern %q", s.Class, s.Pattern)
			}
			if _, bottom := barSpan(t, s.D); bottom > c.Baseline+0.01 {
				t.Errorf("column %d segment %s goes below the baseline: %s", i, s.Class, s.D)
			}
			if top, _ := barSpan(t, s.D); top < padTop-0.01 {
				t.Errorf("column %d segment %s goes above the plot: %s", i, s.Class, s.D)
			}
		}
	}
	if len(c.Cols[1].Segs) != 0 || !strings.Contains(c.Cols[1].Title, "no runs") {
		t.Errorf("an empty day: %+v", c.Cols[1])
	}
	if got := c.Cols[2].Title; got != "Sat Oct 3: 4 runs (1 succeeded, 1 in progress, 1 stale, 1 failed)" {
		t.Errorf("a day's title is %q", got)
	}
	// The numbers are in the table, newest day first, without the empty day.
	if len(c.Table.Rows) != 3 || c.Table.Rows[0][0] != "Sun Oct 4" || c.Table.Rows[0][5] != "8" || c.Table.Rows[1][1] != "1" {
		t.Errorf("table: %+v", c.Table.Rows)
	}
	if !strings.Contains(c.Desc, "16 runs") || !strings.Contains(c.Desc, "busiest day was Oct 4, with 8") {
		t.Errorf("desc: %q", c.Desc)
	}
	// Thin bars with a gap between neighbors, even with a lot of days.
	many := make([]map[string]int, 91)
	for i := range many {
		many[i] = map[string]int{"succeeded": 1}
	}
	c = newBarChart("c", buckets(many...), "last 90 days")
	if w := c.Cols[0].W; w > 8 {
		t.Errorf("90 columns are %v wide", w)
	}
	if len(c.XLabels) > 12 || len(c.XLabels) < 3 {
		t.Errorf("%d x labels", len(c.XLabels))
	}
	if last := c.XLabels[len(c.XLabels)-1]; last.Text != "Dec 30" {
		t.Errorf("the last label is %q", last.Text)
	}
}

func outcomeRank(class string) int {
	for i, o := range outcomeOrder {
		if "o-"+o == class {
			return i
		}
	}
	return -1
}

func TestBarChartOfNothingHasNoNaN(t *testing.T) {
	c := newBarChart("c", buckets(map[string]int{}, map[string]int{}), "last 7 days")
	if len(c.Ticks) == 0 {
		t.Error("no ticks")
	}
	for _, col := range c.Cols {
		if len(col.Segs) != 0 {
			t.Error("segments with no runs")
		}
	}
	for _, tk := range c.Ticks {
		if math.IsNaN(tk.Y) || math.IsInf(tk.Y, 0) {
			t.Errorf("tick %+v", tk)
		}
	}
}

func TestLineChart(t *testing.T) {
	days := buckets(
		map[string]int{"succeeded": 2},              // 0%
		map[string]int{"failed": 1, "succeeded": 1}, // 50%
		map[string]int{},                            // no runs: a gap
		map[string]int{"stale": 1},                  // 100%
		map[string]int{"failed": 1, "succeeded": 2}, // 33%
		map[string]int{"in_progress": 1},            // 0%
	)
	marks := []blueprintMark{
		{SHA: "0123456789abcdef", At: day0().AddDate(0, 0, 1).Add(5 * time.Hour), Subject: "Rework the checklist"},
		{SHA: "fedcba9876543210", At: day0().AddDate(0, 0, 1).Add(9 * time.Hour)}, // the same day: another row
		{SHA: "9999999999999999", At: day0().AddDate(0, 0, -3)},                   // before the window
		{SHA: "8888888888888888", At: day0().AddDate(0, 0, 40)},                   // after it
	}
	c := newLineChart("l", days, marks, "last 7 days")

	// One line through the five days with runs, straight over the day without.
	if !strings.HasPrefix(c.Path, "M") || strings.Count(c.Path, "L") != 4 || strings.Count(c.Path, "M") != 1 {
		t.Errorf("path: %q", c.Path)
	}
	if len(c.Hits) != 6 || len(c.Dots) != 5 {
		t.Errorf("%d hit areas and %d dots", len(c.Hits), len(c.Dots))
	}
	if !strings.Contains(c.Hits[2].Title, "no runs") || c.Hits[1].Title != "Fri Oct 2: 1 run of 2 runs failed or went stale (50%)" {
		t.Errorf("hit titles %q, %q", c.Hits[2].Title, c.Hits[1].Title)
	}
	// The axis goes to 100%, and the end of the line says where it ended.
	if c.Ticks[len(c.Ticks)-1].Label != "100%" || c.EndLabel == nil || c.EndLabel.Text != "0%" {
		t.Errorf("ticks %+v, end label %+v", c.Ticks, c.EndLabel)
	}

	// Markers: the ones in the window, short SHAs, the subject in the tooltip
	// when there is one, and rows so labels never sit on each other.
	if len(c.Markers) != 2 {
		t.Fatalf("%d markers, want 2: %+v", len(c.Markers), c.Markers)
	}
	m0, m1 := c.Markers[0], c.Markers[1]
	if m0.Label != "0123456" || m1.Label != "fedcba9" {
		t.Errorf("labels %q %q", m0.Label, m1.Label)
	}
	if m0.Title != "0123456 first served Fri Oct 2 05:00 UTC: Rework the checklist" || m1.Title != "fedcba9 first served Fri Oct 2 09:00 UTC" {
		t.Errorf("titles %q, %q", m0.Title, m1.Title)
	}
	if m0.X != m1.X || m0.LabelY == m1.LabelY || m0.Anchor != "start" {
		t.Errorf("the same day should share a line but not a row: %+v %+v", m0, m1)
	}
	// The plot starts below the label rows.
	if c.Top <= padTop || m0.LabelY >= c.Top || m1.LabelY >= c.Top {
		t.Errorf("plot top %v, labels at %v and %v", c.Top, m0.LabelY, m1.LabelY)
	}
	if c.PlotH != c.Baseline-c.Top {
		t.Errorf("PlotH %v", c.PlotH)
	}
	// A label that wouldn't fit before the right edge goes to the left of its
	// line, and every label stays inside the chart.
	late := make([]map[string]int, 91)
	for i := range late {
		late[i] = map[string]int{"succeeded": 1}
	}
	edge := newLineChart("l", buckets(late...), []blueprintMark{{SHA: "1111111222222233", At: day0().AddDate(0, 0, 90)}}, "last 90 days")
	if m := edge.Markers[0]; m.Anchor != "end" || m.LabelX >= m.X {
		t.Errorf("a label at the right edge: %+v", m)
	}
	// A label that flips to the left of its line doesn't sit on the one before.
	var crowd []blueprintMark
	for _, d := range []int{70, 84, 88, 90} {
		crowd = append(crowd, blueprintMark{SHA: fmt.Sprintf("%07d111111", d), At: day0().AddDate(0, 0, d)})
	}
	packed := newLineChart("l", buckets(late...), crowd, "last 90 days")
	span := func(m marker) (lo, hi float64) {
		if m.Anchor == "end" {
			return m.LabelX - 52, m.LabelX
		}
		return m.LabelX, m.LabelX + 52
	}
	for i, a := range packed.Markers {
		for _, b := range packed.Markers[i+1:] {
			alo, ahi := span(a)
			blo, bhi := span(b)
			if a.LabelY == b.LabelY && alo < bhi && blo < ahi {
				t.Errorf("labels %s and %s overlap on one row: %+v %+v", a.Label, b.Label, a, b)
			}
		}
	}
	for _, m := range append(c.Markers, edge.Markers...) {
		if m.LabelX < 0 || m.LabelX > chartW {
			t.Errorf("marker label at %v", m.LabelX)
		}
	}
	if !strings.Contains(c.Desc, "peaked at 100% on Oct 4") || !strings.Contains(c.Desc, "2 new blueprint versions are marked") {
		t.Errorf("desc: %q", c.Desc)
	}
	if len(c.Table.Rows) != 5 || c.Table.Rows[0][0] != "Tue Oct 6" || c.Table.Rows[0][4] != "0%" {
		t.Errorf("table: %+v", c.Table.Rows)
	}
}

func TestLineChartWithNoRunsOrOneDay(t *testing.T) {
	c := newLineChart("l", buckets(map[string]int{}, map[string]int{}), nil, "last 7 days")
	if c.Path != "" || c.EndLabel != nil || len(c.Dots) != 0 || len(c.Ticks) != 5 {
		t.Errorf("an empty line chart: %+v", c)
	}
	// One day with runs is a dot, and its path is a point.
	c = newLineChart("l", buckets(map[string]int{}, map[string]int{"failed": 1}, map[string]int{}), nil, "last 7 days")
	if c.Path == "" || strings.Contains(c.Path, "L") || len(c.Dots) != 1 || c.EndLabel == nil || c.EndLabel.Text != "100%" {
		t.Errorf("one day: %+v", c)
	}
	// With many days with runs, only the end of the line gets a dot.
	many := make([]map[string]int, 91)
	for i := range many {
		many[i] = map[string]int{"failed": i % 2, "succeeded": 1}
	}
	c = newLineChart("l", buckets(many...), nil, "last 90 days")
	if len(c.Dots) != 1 || strings.Count(c.Path, "L") != 90 {
		t.Errorf("%d dots over 91 days, path %.30q", len(c.Dots), c.Path)
	}
	// A few days with runs among many without get a dot each, so none vanish.
	sparse := make([]map[string]int, 91)
	for i := range sparse {
		sparse[i] = map[string]int{}
	}
	sparse[3], sparse[40], sparse[80] = map[string]int{"failed": 1}, map[string]int{"succeeded": 1}, map[string]int{"stale": 1}
	if c = newLineChart("l", buckets(sparse...), nil, "last 90 days"); len(c.Dots) != 3 || strings.Count(c.Path, "L") != 2 {
		t.Errorf("a sparse chart has %d dots, path %q", len(c.Dots), c.Path)
	}
	// A low rate gets a closer axis.
	c = newLineChart("l", buckets(map[string]int{"succeeded": 9, "failed": 1}), nil, "last 7 days")
	if last := c.Ticks[len(c.Ticks)-1].Label; last != "10%" && last != "15%" {
		t.Errorf("the axis of a 10%% rate tops out at %s", last)
	}
}

func TestHBarChart(t *testing.T) {
	rows := []hbarRow{{Label: "navigation"}, {Label: "tools"}, {Label: "other"}}
	c := newHBarChart("h", "Friction", rows, []int{10, 5, 0})
	if len(c.Rows) != 3 || c.Rows[2].D != "" || c.Rows[0].D == "" || c.Rows[0].Value != "10" || c.Rows[2].Value != "0" {
		t.Errorf("%+v", c.Rows)
	}
	if c.Rows[0].ValueX <= c.Rows[1].ValueX || c.Rows[1].ValueX <= c.Rows[2].ValueX {
		t.Errorf("bars aren't longest first: %+v", c.Rows)
	}
	if c.Rows[0].ValueX > chartW {
		t.Errorf("a value at %v is off the chart", c.Rows[0].ValueX)
	}
	newHBarChart("h", "Friction", rows, []int{0, 0, 0}) // no division by zero
}

func TestShapesAreWellFormed(t *testing.T) {
	for _, d := range []string{roundedTop(10, 20, 8, 30, 4), roundedTop(10, 20, 8, 2, 4), roundedTop(10, 20, 3, 30, 4), roundedTop(10, 20, 8, 30, 0),
		roundedRight(10, 20, 100, 14, 4), roundedRight(10, 20, 4, 14, 4), roundedRight(10, 20, 100, 14, 0)} {
		if !strings.HasPrefix(d, "M") || !strings.HasSuffix(d, "z") || strings.Contains(d, "NaN") {
			t.Errorf("odd path %q", d)
		}
	}
}
