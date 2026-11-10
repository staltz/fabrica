// Package pace reads a factory's pace, like 30m, 4h or 2d, and turns a
// multiple of it into the cron schedule a runtime installs.
//
// A routine with a pace trigger, like `trigger: { on: pace, every: 4 }`,
// runs every 4 paces of the factory that deploys it: every 2h in a
// factory with `pace: 30m`. Runtimes only know cron, so Fabrica writes the
// period as a cron expression, and only periods that cron can repeat
// evenly are allowed (see Cron).
package pace

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Pace is a factory's pace, in whole minutes. Zero means the factory has
// none.
type Pace int

const (
	hour = 60
	day  = 24 * hour
	week = 7 * day
)

var paceRe = regexp.MustCompile(`^([1-9][0-9]*)([mhd])$`)

// Parse reads a pace like 5m, 4h or 2d: a whole number of minutes, hours
// or days.
func Parse(s string) (Pace, error) {
	m := paceRe.FindStringSubmatch(s)
	if m == nil {
		return 0, fmt.Errorf("%q isn't a pace; write a whole number of minutes, hours or days, like 30m, 4h or 2d", s)
	}
	n, err := strconv.Atoi(m[1])
	if err != nil || n > 365*day {
		return 0, fmt.Errorf("%q is too long; a pace is at most a year", s)
	}
	switch m[2] {
	case "h":
		n *= hour
	case "d":
		n *= day
	}
	if n > 365*day {
		return 0, fmt.Errorf("%q is too long; a pace is at most a year", s)
	}
	return Pace(n), nil
}

// String writes the pace in its largest whole unit, like 30m, 2h or 2d.
func (p Pace) String() string { return Minutes(int(p)) }

// Minutes writes a number of minutes in its largest whole unit, like 30m,
// 2h, 90m or 2d.
func Minutes(n int) string {
	switch {
	case n != 0 && n%day == 0:
		return strconv.Itoa(n/day) + "d"
	case n != 0 && n%hour == 0:
		return strconv.Itoa(n/hour) + "h"
	default:
		return strconv.Itoa(n) + "m"
	}
}

// Times is the pace times a routine's every:, in minutes.
func (p Pace) Times(every int) int { return int(p) * every }

// Cron writes a period, in minutes, as a cron expression (UTC). Cron
// counts within the hour, the day and the month, so it can only repeat
// some periods evenly:
//
//   - a number of minutes that divides an hour (1m, 2m, …, 30m), from
//     minute 0 of each hour;
//   - a number of hours that divides a day (1h, 2h, 3h, 4h, 6h, 8h, 12h),
//     from midnight;
//   - a day, at midnight, or a week, at midnight on Monday;
//   - a number of days up to 31, at midnight, counted from the 1st of each
//     month: cron starts counting again every month, so the last gap of a
//     month can be shorter (every 2d runs on the 31st and again on the
//     1st).
//
// Any other period is an error that suggests the nearest ones that work.
func Cron(minutes int) (string, error) {
	switch {
	case minutes <= 0:
		return "", fmt.Errorf("a period must be positive")
	case minutes == 1:
		return "* * * * *", nil
	case minutes < hour && hour%minutes == 0:
		return fmt.Sprintf("*/%d * * * *", minutes), nil
	case minutes == hour:
		return "0 * * * *", nil
	case minutes < day && minutes%hour == 0 && day%minutes == 0:
		return fmt.Sprintf("0 */%d * * *", minutes/hour), nil
	case minutes == day:
		return "0 0 * * *", nil
	case minutes == week:
		return "0 0 * * 1", nil
	case minutes%day == 0 && minutes/day <= 31:
		return fmt.Sprintf("0 0 */%d * *", minutes/day), nil
	}
	var near []string
	for _, p := range nearest(minutes) {
		near = append(near, "every "+Minutes(p))
	}
	return "", fmt.Errorf("a cron schedule can't repeat every %s evenly; the nearest it can is %s", Minutes(minutes), strings.Join(near, " or "))
}

// nearest finds the periods closest to minutes that Cron can write: the
// one below, and the one above if there is one.
func nearest(minutes int) []int {
	var lower, upper int
	for _, p := range periods() {
		if p < minutes {
			lower = p
		} else if upper == 0 {
			upper = p
		}
	}
	if upper == 0 {
		return []int{lower}
	}
	return []int{lower, upper}
}

// periods lists every period Cron can write, shortest first.
func periods() []int {
	var out []int
	for m := 1; m < hour; m++ {
		if hour%m == 0 {
			out = append(out, m)
		}
	}
	for h := 1; h < 24; h++ {
		if 24%h == 0 {
			out = append(out, h*hour)
		}
	}
	for d := 1; d <= 31; d++ {
		out = append(out, d*day)
	}
	return out
}
