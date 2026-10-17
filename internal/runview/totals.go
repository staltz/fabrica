package runview

import (
	"cmp"
	"slices"

	"github.com/staltz/fabrica/internal/runs"
)

// totals are the numbers at the top of a bundle's README, worked out from
// the runs in the bundle.
type totals struct {
	outcomes map[string]int
	routines []routineTotals
	friction []count
	loads    []loadTotals
	refusals []count
}

type count struct {
	name string
	n    int
}

// routineTotals counts the runs of one routine in one factory. An empty
// routine stands for interactive runs.
type routineTotals struct {
	factory, routine string
	runs             int
	byOutcome        map[string]int
}

// successRate is the share of finished runs that succeeded, in whole
// percent. ok is false when none has finished. Stale and in-progress runs
// aren't finished, so they don't count.
func (r routineTotals) successRate() (percent int, ok bool) {
	done := r.byOutcome[runs.OutcomeSucceeded] + r.byOutcome[runs.OutcomeFailed]
	if done == 0 {
		return 0, false
	}
	return (r.byOutcome[runs.OutcomeSucceeded]*100 + done/2) / done, true
}

// loadTotals counts the runs that loaded one skill, rule or routine.
type loadTotals struct {
	kind, name string
	runs       int
}

// mostLoaded is how many skills and how many rules the README lists.
const mostLoaded = 10

func totalsOf(list []*runs.Detail) totals {
	t := totals{outcomes: map[string]int{}}
	routines := map[[2]string]*routineTotals{}
	friction := map[string]int{}
	loads := map[[2]string]map[string]bool{} // (kind, name) → the runs that loaded it
	refusals := map[string]int{}
	for _, d := range list {
		t.outcomes[d.Outcome]++
		key := [2]string{d.Factory, d.Routine}
		r := routines[key]
		if r == nil {
			r = &routineTotals{factory: d.Factory, routine: d.Routine, byOutcome: map[string]int{}}
			routines[key] = r
		}
		r.runs++
		r.byOutcome[d.Outcome]++
		for _, e := range d.Timeline {
			switch {
			case e.Type == runs.EventFriction:
				friction[e.Kind]++
			case e.Type == runs.EventLoad && e.Kind != runs.LoadRoutine:
				k := [2]string{e.Kind, e.Name}
				if loads[k] == nil {
					loads[k] = map[string]bool{}
				}
				loads[k][d.RunID] = true
			case e.Type == runs.EventCheckpoint && e.Author == runs.ByFabrica && e.Status == runs.Failed:
				refusals[clean(e.Summary)]++
			}
		}
	}
	for _, r := range routines {
		t.routines = append(t.routines, *r)
	}
	slices.SortFunc(t.routines, func(a, b routineTotals) int {
		return cmp.Or(-cmp.Compare(a.runs, b.runs), cmp.Compare(a.factory, b.factory), cmp.Compare(a.routine, b.routine))
	})
	t.friction = sortedCounts(friction)
	t.refusals = sortedCounts(refusals)
	for k, ran := range loads {
		t.loads = append(t.loads, loadTotals{kind: k[0], name: k[1], runs: len(ran)})
	}
	slices.SortFunc(t.loads, func(a, b loadTotals) int {
		return cmp.Or(-cmp.Compare(a.kind, b.kind), -cmp.Compare(a.runs, b.runs), cmp.Compare(a.name, b.name)) // skills, then rules
	})
	// The top few of each kind: skills first, then rules.
	var top []loadTotals
	shown := map[string]int{}
	for _, l := range t.loads {
		if shown[l.kind]++; shown[l.kind] <= mostLoaded {
			top = append(top, l)
		}
	}
	t.loads = top
	return t
}

// sortedCounts turns counts into a list, biggest first.
func sortedCounts(m map[string]int) []count {
	out := make([]count, 0, len(m))
	for name, n := range m {
		out = append(out, count{name, n})
	}
	slices.SortFunc(out, func(a, b count) int { return cmp.Or(-cmp.Compare(a.n, b.n), cmp.Compare(a.name, b.name)) })
	return out
}
