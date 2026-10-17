package dashboard

// The charts are inline SVG. This file works out the geometry in Go, as
// plain structs, and the templates draw it, so every label goes through
// html/template's escaping. Nothing here needs a browser to run.

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/staltz/fabrica/internal/runs"
)

// The size of every chart, in SVG units. The SVG scales down to fit its card,
// but never below minChartWidth CSS pixels wide: past that the chart scrolls
// inside its card, rather than shrinking its text out of sight.
const (
	chartW = 720
	chartH = 260
)

// padding around the plot of a chart.
const (
	padLeft   = 44
	padRight  = 14
	padBottom = 30
	padTop    = 12
)

const (
	segGap   = 2.0  // the surface-colored gap between touching marks
	maxBar   = 24.0 // bars are thin: never wider than this
	barRound = 4.0  // radius of a bar's data end
)

// outcomeOrder is how outcomes stack in a bar, from the baseline up: good
// news at the bottom, bad news on top where it's easiest to see.
var outcomeOrder = []string{runs.OutcomeSucceeded, runs.OutcomeInProgress, runs.OutcomeStale, runs.OutcomeFailed}

// outcomeLabel is how an outcome reads to a person.
func outcomeLabel(outcome string) string {
	switch outcome {
	case runs.OutcomeInProgress:
		return "in progress"
	}
	return outcome
}

type tick struct {
	Y     float64
	Label string
}

type xLabel struct {
	X    float64
	Text string
}

type legendItem struct {
	Class string // like o-failed
	Label string
}

// dataTable is the table that says a chart's numbers in words, for anyone
// who can't hover.
type dataTable struct {
	Summary string // what the toggle says
	Head    []string
	Rows    [][]string
	Note    string
}

// barChart is runs per day, stacked by outcome.
type barChart struct {
	ID, Title, Desc string
	W, H            int
	Baseline        float64
	Left, Right     float64
	Ticks           []tick
	Cols            []barCol
	XLabels         []xLabel
	Legend          []legendItem
	Table           dataTable
}

type barCol struct {
	X, W  float64 // the hit area, a whole slot wide and as tall as the plot
	Title string
	Segs  []barSeg
}

type barSeg struct {
	Class   string
	D       string // the path
	Pattern string // the hatching that stands in for the color in print, empty when there's none
}

// lineChart is a rate per day, with vertical markers.
type lineChart struct {
	ID, Title, Desc string
	W, H            int
	Baseline, Top   float64
	PlotH           float64
	Left, Right     float64
	Ticks           []tick
	Path            string // through the days that have runs
	Dots            []dot
	Hits            []hit
	XLabels         []xLabel
	Markers         []marker
	EndLabel        *endLabel
	Table           dataTable
}

type dot struct {
	X, Y  float64
	Title string
}

type hit struct {
	X, W  float64
	Title string
}

type marker struct {
	X, LabelX, LabelY float64
	HitX, HitH        float64 // a strip around the line, so it's easy to point at
	Anchor            string  // start or end
	Label             string
	Title             string
}

type endLabel struct {
	X, Y float64
	Text string
}

// hbarChart is a few labeled bars, one per row.
type hbarChart struct {
	ID, Title, Desc string
	W, H            int
	Rows            []hbarRow
}

type hbarRow struct {
	RowY, Y, LabelY, ValueX float64
	Label, Value            string
	Title                   string
	D                       string // the bar's path, empty for a zero
}

// dayBucket is one day of a chart.
type dayBucket struct {
	Day      time.Time
	Runs     int
	Outcomes map[string]int
}

// daysBetween lists every UTC day from the day of start to the day of end,
// each filled in from the counts when it has some.
func daysBetween(start, end time.Time, counts []runs.DayCounts) []dayBucket {
	byDay := map[time.Time]runs.DayCounts{}
	for _, c := range counts {
		byDay[c.Day] = c
	}
	var out []dayBucket
	for d := start.UTC().Truncate(24 * time.Hour); !d.After(end.UTC()); d = d.AddDate(0, 0, 1) {
		b := dayBucket{Day: d, Outcomes: map[string]int{}}
		if c, ok := byDay[d]; ok {
			b.Runs, b.Outcomes = c.Runs, c.Outcomes
		}
		out = append(out, b)
	}
	return out
}

// r1 rounds a position to a tenth of a unit, which is plenty and keeps the
// page small.
func r1(x float64) float64 { return math.Round(x*10) / 10 }

// niceScale picks a step and a top for an axis that has to hold max in
// about `want` steps: 1, 2 or 5 times a power of ten, and never less than
// minStep (axes of counts have no half runs).
func niceScale(max float64, want int, minStep float64) (step, top float64) {
	if max <= 0 {
		return minStep, minStep
	}
	raw := math.Max(max/float64(want), minStep)
	mag := math.Pow(10, math.Floor(math.Log10(raw)))
	for _, m := range []float64{1, 2, 5, 10} {
		if step = m * mag; step >= raw {
			break
		}
	}
	return step, math.Ceil(max/step) * step
}

// compact writes a count the short way: 950, 1.2K, 12K.
func compact(n float64) string {
	switch {
	case n >= 10000:
		return fmt.Sprintf("%.0fK", n/1000)
	case n >= 1000:
		return strings.TrimSuffix(fmt.Sprintf("%.1f", n/1000), ".0") + "K"
	}
	return fmt.Sprintf("%.0f", n)
}

// xLabels labels some of the slots with their dates, always including the
// last one, and never closer together than about 64 units.
func xLabels(days []dayBucket, left, slot float64) []xLabel {
	every := int(math.Ceil(64 / slot))
	var out []xLabel
	for i := len(days) - 1; i >= 0; i -= every {
		out = append(out, xLabel{X: r1(left + (float64(i)+0.5)*slot), Text: days[i].Day.Format("Jan 2")})
	}
	// Oldest first, which is the order they read in.
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// roundedTop is the path of a bar that's square at its baseline and round
// at its far end. A radius of zero is a plain rectangle.
func roundedTop(x, y, w, h, r float64) string {
	r = math.Min(r, math.Min(w/2, h))
	if r <= 0 {
		return fmt.Sprintf("M%.1f %.1fh%.1fv%.1fh%.1fz", x, y, w, h, -w)
	}
	return fmt.Sprintf("M%.1f %.1fv%.1fa%.1f %.1f 0 0 1 %.1f %.1fh%.1fa%.1f %.1f 0 0 1 %.1f %.1fv%.1fz",
		x, y+h, -(h - r), r, r, r, -r, w-2*r, r, r, r, r, h-r)
}

// roundedRight is the same for a bar that grows to the right.
func roundedRight(x, y, w, h, r float64) string {
	r = math.Min(r, math.Min(h/2, w))
	if r <= 0 {
		return fmt.Sprintf("M%.1f %.1fh%.1fv%.1fh%.1fz", x, y, w, h, -w)
	}
	return fmt.Sprintf("M%.1f %.1fh%.1fa%.1f %.1f 0 0 1 %.1f %.1fv%.1fa%.1f %.1f 0 0 1 %.1f %.1fh%.1fz",
		x, y, w-r, r, r, r, r, h-2*r, r, r, -r, r, -(w - r))
}

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

// dayTitle is what hovering a day says.
func dayTitle(b dayBucket) string {
	if b.Runs == 0 {
		return b.Day.Format("Mon Jan 2") + ": no runs"
	}
	var parts []string
	for _, o := range outcomeOrder {
		if n := b.Outcomes[o]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, outcomeLabel(o)))
		}
	}
	return fmt.Sprintf("%s: %s (%s)", b.Day.Format("Mon Jan 2"), plural(b.Runs, "run", "runs"), strings.Join(parts, ", "))
}

// newBarChart draws the days as stacked bars.
func newBarChart(id string, days []dayBucket, window string) barChart {
	c := barChart{
		ID: id, Title: "Runs per day, by outcome", W: chartW, H: chartH,
		Left: padLeft, Right: chartW - padRight, Baseline: chartH - padBottom,
	}
	var total, busiest int
	var busiestDay time.Time
	maxDay := 0
	for _, b := range days {
		total += b.Runs
		if b.Runs > maxDay {
			maxDay, busiest, busiestDay = b.Runs, b.Runs, b.Day
		}
	}
	c.Desc = fmt.Sprintf("%s in the %s.", plural(total, "run", "runs"), window)
	if total > 0 {
		c.Desc += fmt.Sprintf(" The busiest day was %s, with %d.", busiestDay.Format("Jan 2"), busiest)
	}
	for _, o := range outcomeOrder {
		c.Legend = append(c.Legend, legendItem{Class: "o-" + o, Label: outcomeLabel(o)})
	}

	plotW := c.Right - c.Left
	plotH := c.Baseline - padTop
	step, top := niceScale(float64(maxDay), 4, 1)
	for v := 0.0; v <= top+step/2; v += step {
		c.Ticks = append(c.Ticks, tick{Y: r1(c.Baseline - v/top*plotH), Label: compact(v)})
	}
	slot := plotW / float64(len(days))
	bw := math.Min(maxBar, math.Max(2, slot-segGap))
	for i, b := range days {
		col := barCol{X: r1(c.Left + float64(i)*slot), W: r1(slot), Title: dayTitle(b)}
		x := c.Left + (float64(i)+0.5)*slot - bw/2
		// Stack the outcomes from the baseline up, with a gap between
		// neighbors. Only the top of the stack gets a round data end.
		type rect struct {
			class string
			y, h  float64
		}
		var stack []rect
		cum := 0
		for _, o := range outcomeOrder {
			n := b.Outcomes[o]
			if n == 0 {
				continue
			}
			bottom := c.Baseline - float64(cum)/top*plotH
			cum += n
			topY := c.Baseline - float64(cum)/top*plotH
			if len(stack) > 0 {
				bottom -= segGap
			}
			h := math.Max(bottom-topY, 1.5)
			stack = append(stack, rect{class: "o-" + o, y: bottom - h, h: h})
		}
		var segs []barSeg
		for j, r := range stack {
			radius := 0.0
			if j == len(stack)-1 {
				radius = barRound
			}
			seg := barSeg{Class: r.class, D: roundedTop(x, r.y, bw, r.h, radius)}
			if r.class != "o-"+runs.OutcomeSucceeded {
				seg.Pattern = strings.TrimPrefix(r.class, "o-")
			}
			segs = append(segs, seg)
		}
		col.Segs = segs
		c.Cols = append(c.Cols, col)
	}
	c.XLabels = xLabels(days, c.Left, slot)

	c.Table = dataTable{
		Summary: "Show the numbers as a table",
		Head:    []string{"Day (UTC)", "Succeeded", "In progress", "Stale", "Failed", "Runs"},
		Note:    "Days with no runs are left out.",
	}
	for i := len(days) - 1; i >= 0; i-- {
		b := days[i]
		if b.Runs == 0 {
			continue
		}
		c.Table.Rows = append(c.Table.Rows, []string{b.Day.Format("Mon Jan 2"),
			fmt.Sprint(b.Outcomes[runs.OutcomeSucceeded]), fmt.Sprint(b.Outcomes[runs.OutcomeInProgress]),
			fmt.Sprint(b.Outcomes[runs.OutcomeStale]), fmt.Sprint(b.Outcomes[runs.OutcomeFailed]), fmt.Sprint(b.Runs)})
	}
	return c
}

// A blueprintMark is a blueprint commit that was first served on some day.
type blueprintMark struct {
	SHA     string
	At      time.Time
	Subject string // empty when unknown
}

// failureRate is the share of a day's runs that failed or went stale.
func failureRate(b dayBucket) float64 {
	if b.Runs == 0 {
		return 0
	}
	return float64(b.Outcomes[runs.OutcomeFailed]+b.Outcomes[runs.OutcomeStale]) / float64(b.Runs)
}

func percent(rate float64) string { return fmt.Sprintf("%.0f%%", rate*100) }

// newLineChart draws the failure rate per day, with a vertical line where
// each blueprint commit was first served.
func newLineChart(id string, days []dayBucket, marks []blueprintMark, window string) lineChart {
	c := lineChart{
		ID: id, Title: "Failure rate per day", W: chartW, H: chartH,
		Left: padLeft, Right: chartW - padRight - 28, // room for the end label
		Baseline: chartH - padBottom,
	}

	// Markers closer than a label's width go on different rows. A label
	// starts at its line, or ends there when it wouldn't fit before the
	// right edge.
	const labelW, rowH = 52.0, 13.0
	slotW := (c.Right - c.Left) / float64(len(days))
	xOf := func(i int) float64 { return c.Left + (float64(i)+0.5)*slotW }
	var rowEnds []float64
	for _, m := range marks {
		i := int(m.At.UTC().Truncate(24*time.Hour).Sub(days[0].Day) / (24 * time.Hour))
		if i < 0 || i >= len(days) {
			continue // not in this window
		}
		x := xOf(i)
		anchor, lo, hi := "start", x+4, x+4+labelW
		if hi > chartW-4 {
			anchor, lo, hi = "end", x-4-labelW, x-4
		}
		row := 0
		for row < len(rowEnds) && rowEnds[row] > lo-4 {
			row++
		}
		if row == len(rowEnds) {
			rowEnds = append(rowEnds, hi)
		} else {
			rowEnds[row] = hi
		}
		lx := lo
		if anchor == "end" {
			lx = hi
		}
		title := fmt.Sprintf("%s first served %s", shortSHA(m.SHA), m.At.UTC().Format("Mon Jan 2 15:04 UTC"))
		if m.Subject != "" {
			title += ": " + m.Subject
		}
		c.Markers = append(c.Markers, marker{X: r1(x), LabelX: r1(lx), LabelY: float64(row) * rowH, HitX: r1(x - 6), Anchor: anchor, Label: shortSHA(m.SHA), Title: title})
	}
	c.Top = padTop + float64(len(rowEnds))*rowH
	plotH := c.Baseline - c.Top
	c.PlotH = plotH
	for i := range c.Markers {
		c.Markers[i].LabelY += padTop - 2 // above the plot, first row on top
		c.Markers[i].HitH = c.Baseline - c.Markers[i].LabelY
	}

	maxRate, withRuns := 0.0, 0
	var worst dayBucket
	for _, b := range days {
		if b.Runs > 0 {
			withRuns++
		}
		if r := failureRate(b); b.Runs > 0 && r >= maxRate {
			maxRate, worst = r, b
		}
	}
	step, top := niceScale(maxRate*100, 4, 1)
	top = math.Min(top, 100)
	if maxRate == 0 {
		step, top = 25, 100
	}
	for v := 0.0; v <= top+step/2; v += step {
		c.Ticks = append(c.Ticks, tick{Y: r1(c.Baseline - v/top*plotH), Label: fmt.Sprintf("%.0f%%", v)})
	}

	c.Desc = fmt.Sprintf("Share of each day's runs that failed or went stale, in the %s.", window)
	if withRuns > 0 {
		c.Desc += fmt.Sprintf(" It peaked at %s on %s.", percent(maxRate), worst.Day.Format("Jan 2"))
	}
	if len(c.Markers) > 0 {
		c.Desc += fmt.Sprintf(" %s marked.", plural(len(c.Markers), "new blueprint version is", "new blueprint versions are"))
	}

	// The line goes through the days that have runs, and straight over the
	// days that don't. Each day's dot says what it was, as long as there are
	// few enough to tell apart; otherwise only the end gets one.
	var path strings.Builder
	var last dot
	for i, b := range days {
		c.Hits = append(c.Hits, hit{X: r1(c.Left + float64(i)*slotW), W: r1(slotW), Title: lineTitle(b)})
		if b.Runs == 0 {
			continue
		}
		x, y := r1(xOf(i)), r1(c.Baseline-failureRate(b)*100/top*plotH)
		if path.Len() == 0 {
			fmt.Fprintf(&path, "M%.1f %.1f", x, y)
		} else {
			fmt.Fprintf(&path, "L%.1f %.1f", x, y)
		}
		last = dot{X: x, Y: y, Title: lineTitle(b)}
		if withRuns <= maxDots {
			c.Dots = append(c.Dots, last)
		}
	}
	c.Path = path.String()
	if withRuns > 0 {
		if withRuns > maxDots {
			c.Dots = append(c.Dots, last)
		}
		c.EndLabel = &endLabel{X: last.X + 9, Y: last.Y + 4, Text: percent(failureRate(lastWithRuns(days)))}
	}
	c.XLabels = xLabels(days, c.Left, slotW)

	c.Table = dataTable{
		Summary: "Show the numbers as a table",
		Head:    []string{"Day (UTC)", "Runs", "Failed", "Stale", "Failure rate"},
		Note:    "Days with no runs are left out.",
	}
	for i := len(days) - 1; i >= 0; i-- {
		b := days[i]
		if b.Runs == 0 {
			continue
		}
		c.Table.Rows = append(c.Table.Rows, []string{b.Day.Format("Mon Jan 2"), fmt.Sprint(b.Runs),
			fmt.Sprint(b.Outcomes[runs.OutcomeFailed]), fmt.Sprint(b.Outcomes[runs.OutcomeStale]), percent(failureRate(b))})
	}
	return c
}

// maxDots is the most days a line chart gives a dot each.
const maxDots = 40

func lastWithRuns(days []dayBucket) dayBucket {
	for i := len(days) - 1; i >= 0; i-- {
		if days[i].Runs > 0 {
			return days[i]
		}
	}
	return dayBucket{}
}

func lineTitle(b dayBucket) string {
	if b.Runs == 0 {
		return b.Day.Format("Mon Jan 2") + ": no runs"
	}
	bad := b.Outcomes[runs.OutcomeFailed] + b.Outcomes[runs.OutcomeStale]
	return fmt.Sprintf("%s: %s of %s failed or went stale (%s)", b.Day.Format("Mon Jan 2"), plural(bad, "run", "runs"), plural(b.Runs, "run", "runs"), percent(failureRate(b)))
}

// shortSHA is the first 7 characters of a commit, the way git prints them.
func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// newHBarChart draws a few counts as horizontal bars, with each count at the
// tip of its bar.
func newHBarChart(id, title string, rows []hbarRow, counts []int) hbarChart {
	const rowH, labelW, tipW = 30.0, 96.0, 40.0
	c := hbarChart{ID: id, Title: title, W: chartW, H: int(rowH)*len(rows) + 4}
	maxN := 0
	for _, n := range counts {
		maxN = max(maxN, n)
	}
	avail := chartW - labelW - tipW - padRight
	for i, r := range rows {
		y := 2 + float64(i)*rowH
		r.RowY, r.Y, r.LabelY = y, y+(rowH-14)/2, y+rowH/2+4
		w := 0.0
		if maxN > 0 {
			w = float64(counts[i]) / float64(maxN) * avail
		}
		if counts[i] > 0 {
			w = math.Max(w, 4)
			r.D = roundedRight(labelW, r.Y, w, 14, barRound)
		}
		r.ValueX = r1(labelW + w + 8)
		r.Value = fmt.Sprint(counts[i])
		c.Rows = append(c.Rows, r)
	}
	return c
}
