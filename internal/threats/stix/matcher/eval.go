package matcher

import (
	"time"

	"github.com/observiq/bindplane-otel-collector/internal/threats/stix/pattern"
)

// instanceID identifies one "instance" of an observation (for number_observed).
type instanceID struct {
	obsIdx  int
	instIdx int
}

// binding is a tuple of instance IDs (one per observation expression in the pattern).
type binding []instanceID

// eval evaluates the pattern AST against observations and returns whether it matched
// and the first binding's observation indices (for SDO result).
func eval(pat *pattern.Pattern, observations []*observation) (matched bool, firstBinding []int) {
	if pat == nil || pat.ObservationExpressions == nil || len(observations) == 0 {
		return false, nil
	}
	bindings := evalObservationExpressions(pat.ObservationExpressions, observations)
	for b := range bindings {
		// Return first binding: collect unique obs indices in order.
		seen := make(map[int]bool)
		var sdos []int
		for _, id := range b {
			if id.obsIdx >= 0 && !seen[id.obsIdx] {
				seen[id.obsIdx] = true
				sdos = append(sdos, id.obsIdx)
			}
		}
		return true, sdos
	}
	return false, nil
}

// evalObservationExpressions yields bindings for FOLLOWEDBY sequence.
func evalObservationExpressions(expr *pattern.ObservationExpressions, obs []*observation) <-chan binding {
	out := make(chan binding, 1)
	go func() {
		defer close(out)
		if expr == nil || len(expr.Ors) == 0 {
			return
		}
		// Single Or: no FOLLOWEDBY
		if len(expr.Ors) == 1 {
			for b := range evalObservationExpressionOr(expr.Ors[0], obs) {
				out <- b
			}
			return
		}
		// FOLLOWEDBY: join LHS and RHS with timestamp constraint
		var lhsCh <-chan binding
		for i, or := range expr.Ors {
			rhsCh := evalObservationExpressionOr(or, obs)
			if i == 0 {
				lhsCh = rhsCh
				continue
			}
			// Join lhsCh and rhsCh: disjoint + LHS last <= RHS first
			joined := make(chan binding, 1)
			go func(lhs <-chan binding, rhs <-chan binding, observations []*observation) {
				defer close(joined)
				var lhsList []binding
				for b := range lhs {
					lhsList = append(lhsList, b)
				}
				for rb := range rhs {
					for _, lb := range lhsList {
						if !bindingDisjoint(lb, rb) {
							continue
						}
						if !timestampOrderingOK(lb, rb, observations) {
							continue
						}
						joined <- append(append(binding(nil), lb...), rb...)
					}
				}
			}(lhsCh, rhsCh, obs)
			lhsCh = joined
		}
		for b := range lhsCh {
			out <- b
		}
	}()
	return out
}

func timestampOrderingOK(lhs, rhs binding, obs []*observation) bool {
	// Latest LHS first_observed <= earliest RHS last_observed
	var latestLHS time.Time
	var earliestRHS time.Time
	first := true
	for _, id := range lhs {
		if id.obsIdx < 0 || id.obsIdx >= len(obs) {
			continue
		}
		t, _ := time.Parse(time.RFC3339Nano, obs[id.obsIdx].timeInterval[0])
		if first || t.After(latestLHS) {
			latestLHS = t
			first = false
		}
	}
	first = true
	for _, id := range rhs {
		if id.obsIdx < 0 || id.obsIdx >= len(obs) {
			continue
		}
		t, _ := time.Parse(time.RFC3339Nano, obs[id.obsIdx].timeInterval[1])
		if first || t.Before(earliestRHS) {
			earliestRHS = t
			first = false
		}
	}
	return !latestLHS.After(earliestRHS)
}

func bindingDisjoint(a, b binding) bool {
	seen := make(map[instanceID]bool)
	for _, id := range a {
		seen[id] = true
	}
	for _, id := range b {
		if seen[id] {
			return false
		}
	}
	return true
}

func evalObservationExpressionOr(expr *pattern.ObservationExpressionOr, obs []*observation) <-chan binding {
	out := make(chan binding, 1)
	go func() {
		defer close(out)
		if expr == nil {
			return
		}
		for _, andExpr := range expr.Ands {
			for b := range evalObservationExpressionAnd(andExpr, obs) {
				out <- b
			}
		}
	}()
	return out
}

func evalObservationExpressionAnd(expr *pattern.ObservationExpressionAnd, obs []*observation) <-chan binding {
	out := make(chan binding, 1)
	go func() {
		defer close(out)
		if expr == nil || len(expr.Exprs) == 0 {
			return
		}
		chans := make([]<-chan binding, len(expr.Exprs))
		for i, e := range expr.Exprs {
			chans[i] = evalObservationExpression(e, obs)
		}
		// Cartesian product of bindings that are pairwise disjoint
		joinAnd(chans, out, expr.Exprs, obs)
	}()
	return out
}

func joinAnd(chans []<-chan binding, out chan binding, exprs []*pattern.ObservationExpression, obs []*observation) {
	if len(chans) == 0 {
		return
	}
	if len(chans) == 1 {
		for b := range chans[0] {
			out <- b
		}
		return
	}
	// Collect first channel
	var first []binding
	for b := range chans[0] {
		first = append(first, b)
	}
	// Recursively join rest
	restCh := make(chan binding, 1)
	go func() {
		defer close(restCh)
		joinAnd(chans[1:], restCh, exprs[1:], obs)
	}()
	for lb := range restCh {
		for _, rb := range first {
			if bindingDisjoint(lb, rb) {
				// First expr first, then rest (pattern order)
				out <- append(append(binding(nil), rb...), lb...)
			}
		}
	}
}

func evalObservationExpression(expr *pattern.ObservationExpression, obs []*observation) <-chan binding {
	out := make(chan binding, 1)
	go func() {
		defer close(out)
		if expr == nil {
			return
		}
		var base <-chan binding
		if expr.Comparison != nil {
			base = evalComparisonExpression(expr.Comparison, obs)
		} else if expr.Nested != nil {
			base = evalObservationExpressions(expr.Nested, obs)
		} else {
			return
		}
		// Apply qualifiers
		if expr.Repeats != nil {
			n := expr.Repeats.Times
			if n < 1 {
				return
			}
			base = evalRepeats(base, n)
		}
		if expr.Within != nil {
			sec := 0.0
			if expr.Within.Seconds.Float != nil {
				sec = *expr.Within.Seconds.Float
			}
			base = evalWithin(base, obs, sec)
		}
		if expr.StartStop != nil {
			base = evalStartStop(base, obs, expr.StartStop)
		}
		for b := range base {
			out <- b
		}
	}()
	return out
}

func evalRepeats(in <-chan binding, n int) <-chan binding {
	out := make(chan binding, 1)
	go func() {
		defer close(out)
		var list []binding
		for b := range in {
			list = append(list, b)
		}
		// All n-sized disjoint combinations
		for _, combo := range disjointCombinations(list, n) {
			var flat binding
			for _, b := range combo {
				flat = append(flat, b...)
			}
			out <- flat
		}
	}()
	return out
}

func disjointCombinations(list []binding, n int) [][]binding {
	if n <= 0 || n > len(list) {
		return nil
	}
	if n == 1 {
		out := make([][]binding, len(list))
		for i, b := range list {
			out[i] = []binding{b}
		}
		return out
	}
	var out [][]binding
	for i := 0; i <= len(list)-n; i++ {
		for _, sub := range disjointCombinations(disjointFrom(list[i+1:], list[i]), n-1) {
			out = append(out, append([]binding{list[i]}, sub...))
		}
	}
	return out
}

func disjointFrom(list []binding, b binding) []binding {
	seen := make(map[instanceID]bool)
	for _, id := range b {
		seen[id] = true
	}
	var out []binding
	for _, x := range list {
		disj := true
		for _, id := range x {
			if seen[id] {
				disj = false
				break
			}
		}
		if disj {
			out = append(out, x)
		}
	}
	return out
}

func evalWithin(in <-chan binding, obs []*observation, seconds float64) <-chan binding {
	out := make(chan binding, 1)
	go func() {
		defer close(out)
		dur := time.Duration(seconds * float64(time.Second))
		for b := range in {
			intervals := make([][2]time.Time, 0, len(b))
			for _, id := range b {
				if id.obsIdx < 0 || id.obsIdx >= len(obs) {
					continue
				}
				o := obs[id.obsIdx]
				t0, _ := time.Parse(time.RFC3339Nano, o.timeInterval[0])
				t1, _ := time.Parse(time.RFC3339Nano, o.timeInterval[1])
				intervals = append(intervals, [2]time.Time{t0, t1})
			}
			if timestampIntervalsWithin(intervals, dur) {
				out <- b
			}
		}
	}()
	return out
}

func timestampIntervalsWithin(intervals [][2]time.Time, dur time.Duration) bool {
	if len(intervals) == 0 {
		return true
	}
	// Find earliest last_observed, then check an interval of dur starting there overlaps all
	minLast := intervals[0][1]
	for _, iv := range intervals[1:] {
		if iv[1].Before(minLast) {
			minLast = iv[1]
		}
	}
	start := minLast
	end := minLast.Add(dur)
	for _, iv := range intervals {
		// Overlap [iv[0], iv[1]] with [start, end]
		if iv[1].Before(start) || iv[0].After(end) {
			return false
		}
	}
	return true
}

func evalStartStop(in <-chan binding, obs []*observation, q *pattern.StartStopQualifier) <-chan binding {
	out := make(chan binding, 1)
	go func() {
		defer close(out)
		if q.Start.Timestamp == nil || q.Stop.Timestamp == nil {
			return
		}
		startS := *q.Start.Timestamp
		stopS := *q.Stop.Timestamp
		if startS == "" || stopS == "" {
			return
		}
		startT, err1 := parseTimestamp(startS)
		stopT, err2 := parseTimestamp(stopS)
		if err1 != nil || err2 != nil || !startT.Before(stopT) {
			return
		}
		for b := range in {
			ok := true
			for _, id := range b {
				if id.obsIdx < 0 || id.obsIdx >= len(obs) {
					continue
				}
				o := obs[id.obsIdx]
				t0, _ := time.Parse(time.RFC3339Nano, o.timeInterval[0])
				t1, _ := time.Parse(time.RFC3339Nano, o.timeInterval[1])
				// Overlap with [startT, stopT); touch at start ok, touch at stop not ok
				if t1.Before(startT) || !t0.Before(stopT) {
					ok = false
					break
				}
			}
			if ok {
				out <- b
			}
		}
	}()
	return out
}

func evalComparisonExpression(expr *pattern.ComparisonExpression, obs []*observation) <-chan binding {
	out := make(chan binding, 1)
	go func() {
		defer close(out)
		if expr == nil {
			return
		}
		if len(expr.Ands) == 0 {
			return
		}
		if len(expr.Ands) == 1 {
			for b := range evalComparisonExpressionAnd(expr.Ands[0], obs) {
				out <- b
			}
			return
		}
		// OR: union of bindings from each And
		for _, and := range expr.Ands {
			for b := range evalComparisonExpressionAnd(and, obs) {
				out <- b
			}
		}
	}()
	return out
}

func evalComparisonExpressionAnd(expr *pattern.ComparisonExpressionAnd, obs []*observation) <-chan []instanceID {
	out := make(chan []instanceID, 1)
	go func() {
		defer close(out)
		if expr == nil || len(expr.PropTests) == 0 {
			return
		}
		obsMap := evalPropTest(expr.PropTests[0], obs)
		if obsMap == nil {
			return
		}
		for _, pt := range expr.PropTests[1:] {
			passed := evalPropTest(pt, obs)
			if passed == nil {
				return
			}
			for obsIdx, roots := range obsMap {
				for rootID := range roots {
					if _, ok := passed[obsIdx][rootID]; !ok {
						delete(roots, rootID)
					}
				}
				if len(roots) == 0 {
					delete(obsMap, obsIdx)
				}
			}
		}
		// Emit one binding per (obsIdx, instIdx) for 0..number_observed-1
		for obsIdx, roots := range obsMap {
			if len(roots) == 0 {
				continue
			}
			o := obs[obsIdx]
			n := o.numberObserved
			if n < 1 {
				n = 1
			}
			for instIdx := 0; instIdx < n; instIdx++ {
				out <- []instanceID{{obsIdx: obsIdx, instIdx: instIdx}}
			}
		}
	}()
	return out
}

// evalPropTest returns obsIdx -> rootObjectID set for observations that passed the test.
func evalPropTest(pt pattern.PropTest, obs []*observation) map[int]map[string]struct{} {
	switch p := pt.(type) {
	case *pattern.PropTestEqual:
		return evalPropTestEqual(p, obs)
	case *pattern.PropTestOrder:
		return evalPropTestOrder(p, obs)
	case *pattern.PropTestSet:
		return evalPropTestSet(p, obs)
	case *pattern.PropTestLike:
		return evalPropTestLike(p, obs)
	case *pattern.PropTestRegex:
		return evalPropTestRegex(p, obs)
	case *pattern.PropTestIsSubset:
		return evalPropTestIsSubset(p, obs)
	case *pattern.PropTestIsSuperset:
		return evalPropTestIsSuperset(p, obs)
	case *pattern.PropTestExists:
		return evalPropTestExists(p, obs)
	case *pattern.PropTestParens:
		return evalPropTestParens(p, obs)
	}
	return nil
}

func obsMapPropTest(obs []*observation, path *pattern.ObjectPath, pred func(interface{}) bool) map[int]map[string]struct{} {
	result := make(map[int]map[string]struct{})
	for i, o := range obs {
		vals := o.resolveObjectPath(path.ObjectType, path.Path)
		if vals == nil {
			continue
		}
		for rootID, list := range vals {
			for _, v := range list {
				if pred(v) {
					if result[i] == nil {
						result[i] = make(map[string]struct{})
					}
					result[i][rootID] = struct{}{}
					break
				}
			}
		}
	}
	return result
}

func evalPropTestEqual(p *pattern.PropTestEqual, obs []*observation) map[int]map[string]struct{} {
	negate := (p.Op == "!=") || p.Not
	return obsMapPropTest(obs, p.ObjectPath, func(v interface{}) bool {
		return compareEq(v, p.Literal, negate)
	})
}

func evalPropTestOrder(p *pattern.PropTestOrder, obs []*observation) map[int]map[string]struct{} {
	return obsMapPropTest(obs, p.ObjectPath, func(v interface{}) bool {
		return compareOrder(v, p.Literal, p.Op, p.Not)
	})
}

func evalPropTestSet(p *pattern.PropTestSet, obs []*observation) map[int]map[string]struct{} {
	return obsMapPropTest(obs, p.ObjectPath, func(v interface{}) bool {
		return compareIn(v, p.Set, p.Not)
	})
}

func evalPropTestLike(p *pattern.PropTestLike, obs []*observation) map[int]map[string]struct{} {
	return obsMapPropTest(obs, p.ObjectPath, func(v interface{}) bool {
		return compareLike(v, p.Pattern, p.Not)
	})
}

func evalPropTestRegex(p *pattern.PropTestRegex, obs []*observation) map[int]map[string]struct{} {
	return obsMapPropTest(obs, p.ObjectPath, func(v interface{}) bool {
		return compareMatches(v, p.Regex, p.Not)
	})
}

func evalPropTestIsSubset(p *pattern.PropTestIsSubset, obs []*observation) map[int]map[string]struct{} {
	return obsMapPropTest(obs, p.ObjectPath, func(v interface{}) bool {
		return compareIsSubset(v, p.Value, p.Not)
	})
}

func evalPropTestIsSuperset(p *pattern.PropTestIsSuperset, obs []*observation) map[int]map[string]struct{} {
	return obsMapPropTest(obs, p.ObjectPath, func(v interface{}) bool {
		return compareIsSuperset(v, p.Value, p.Not)
	})
}

func evalPropTestExists(p *pattern.PropTestExists, obs []*observation) map[int]map[string]struct{} {
	result := make(map[int]map[string]struct{})
	for i, o := range obs {
		vals := o.resolveObjectPath(p.ObjectPath.ObjectType, p.ObjectPath.Path)
		if vals == nil {
			if p.Not {
				// NOT EXISTS: no path match means "exists" is false, so NOT EXISTS is true - include all?
				// Per spec: EXISTS path is true if path resolves to at least one value. So NOT EXISTS is true if path resolves to zero.
				// We don't have "all root objects" here; we'd need to list all object IDs of the right type. Skip for now.
			}
			continue
		}
		for rootID := range vals {
			exists := len(vals[rootID]) > 0
			if exists != p.Not {
				if result[i] == nil {
					result[i] = make(map[string]struct{})
				}
				result[i][rootID] = struct{}{}
			}
		}
	}
	return result
}

func evalPropTestParens(p *pattern.PropTestParens, obs []*observation) map[int]map[string]struct{} {
	// Nested comparison: evaluate and return (obsIdx, rootID) from any matching binding
	result := make(map[int]map[string]struct{})
	for b := range evalComparisonExpression(p.Comparison, obs) {
		for _, id := range b {
			if id.obsIdx >= 0 {
				if result[id.obsIdx] == nil {
					result[id.obsIdx] = make(map[string]struct{})
				}
				result[id.obsIdx][""] = struct{}{}
			}
		}
	}
	return result
}
