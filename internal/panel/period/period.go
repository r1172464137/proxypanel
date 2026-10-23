// Package period implements the frozen monthly billing window algorithm
// (period/v1): a cycle starts at local midnight of anchor_day in the business
// timezone, clipped to the last day of short months, with deterministic DST
// handling. The same implementation feeds the panel authorization, subscription
// output and the agent snapshot.
package period

import (
	"fmt"
	"math/big"
	"sort"
	"time"
	_ "time/tzdata" // embed IANA data so LoadLocation works on any host
)

// AlgorithmVersion identifies the cycle algorithm on the wire.
const AlgorithmVersion = "period/v1"

// Boundary is a half open cycle window [Start, End).
type Boundary struct {
	Start time.Time
	End   time.Time
}

// Allocation is one ledger slice for a sample that may straddle boundaries.
type Allocation struct {
	Period    Boundary
	UpBytes   int64
	DownBytes int64
	Estimated bool
	Split     bool
}

// LoadTimezone resolves an explicit IANA name. Empty or unknown names are an
// error: the panel never infers a timezone from the host or the browser.
func LoadTimezone(name string) (*time.Location, error) {
	if name == "" {
		return nil, fmt.Errorf("timezone is required")
	}
	if name == "Local" {
		return nil, fmt.Errorf("explicit IANA timezone is required; Local depends on the host")
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, fmt.Errorf("unknown timezone %q", name)
	}
	return loc, nil
}

// DaysIn returns the number of days in the month.
func DaysIn(year int, month time.Month) int {
	return time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

// ClampDay clips anchor_day to the number of days in the given month, which is
// what makes anchor_day=31 land on 28/29 February without special cases.
func ClampDay(anchorDay int, year int, month time.Month) int {
	if anchorDay < 1 {
		anchorDay = 1
	}
	if max := DaysIn(year, month); anchorDay > max {
		return max
	}
	return anchorDay
}

// AnchorInstant returns the UTC instant of local midnight on the clipped
// anchor day. If midnight does not exist (DST spring forward) the first valid
// instant after the gap is used; if midnight happens twice (DST fall back) the
// earlier instant wins.
func AnchorInstant(anchorDay int, loc *time.Location, year int, month time.Month) time.Time {
	if loc == nil {
		loc = time.UTC
	}
	return resolveWallClock(year, month, ClampDay(anchorDay, year, month), loc)
}

// resolveWallClock maps a local wall clock date at 00:00 to a UTC instant.
func resolveWallClock(year int, month time.Month, day int, loc *time.Location) time.Time {
	wall := time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
	_, offset := wall.In(loc).Zone()
	guess := wall.Add(-time.Duration(offset) * time.Second)
	// Two days on either side cover IANA date-line skips as well as DST gaps.
	cursor, limit := guess.Add(-48*time.Hour), guess.Add(48*time.Hour)
	var exact, afterGap time.Time
	var hasExact, hasGap bool
	for !cursor.After(limit) {
		local := cursor.In(loc)
		_, offset := local.Zone()
		_, end := local.ZoneBounds()
		candidate := wall.Add(-time.Duration(offset) * time.Second)
		// The local clock is linear only within one constant-offset interval.
		if !candidate.Before(cursor) && (end.IsZero() || candidate.Before(end)) {
			if !hasExact || candidate.Before(exact) {
				exact, hasExact = candidate, true
			}
		} else if candidate.Before(cursor) && (!hasGap || cursor.Before(afterGap)) {
			afterGap, hasGap = cursor, true
		}
		if end.IsZero() || !end.After(cursor) {
			break
		}
		cursor = end
	}
	// Prefer an actual midnight; repeated midnights use the earlier instant.
	if hasExact {
		return exact.UTC()
	}
	if hasGap {
		return afterGap.UTC()
	}
	return guess.UTC()
}

// NextBoundary returns the first anchor instant strictly after `after`.
func NextBoundary(after time.Time, anchorDay int, loc *time.Location) time.Time {
	if loc == nil {
		loc = time.UTC
	}
	base := after.UTC()
	var best time.Time
	for delta := -1; delta <= 3; delta++ {
		month := base.Month() + time.Month(delta)
		year := base.Year()
		for month < time.January {
			month += 12
			year--
		}
		for month > time.December {
			month -= 12
			year++
		}
		cand := AnchorInstant(anchorDay, loc, year, month)
		if !cand.After(base) {
			continue
		}
		if best.IsZero() || cand.Before(best) {
			best = cand
		}
	}
	if best.IsZero() {
		// Fall back to a month later; unreachable for sane anchor days.
		return base.AddDate(0, 1, 0)
	}
	return best
}

// Containing returns the cycle window that contains t.
func Containing(anchorDay int, loc *time.Location, t time.Time) Boundary {
	if loc == nil {
		loc = time.UTC
	}
	at := t.UTC()
	base := at.In(loc)
	var start time.Time
	for delta := 0; delta >= -2; delta-- {
		month := base.Month() + time.Month(delta)
		year := base.Year()
		for month < time.January {
			month += 12
			year--
		}
		cand := AnchorInstant(anchorDay, loc, year, month)
		if !cand.After(at) {
			if start.IsZero() || cand.After(start) {
				start = cand
			}
		}
	}
	if start.IsZero() {
		start = AnchorInstant(anchorDay, loc, base.Year(), base.Month())
	}
	return Boundary{Start: start, End: NextBoundary(start, anchorDay, loc)}
}

// PrevBoundary returns the last anchor instant strictly before `before`.
func PrevBoundary(before time.Time, anchorDay int, loc *time.Location) time.Time {
	if loc == nil {
		loc = time.UTC
	}
	base := before.In(loc)
	var best time.Time
	for delta := 0; delta >= -3; delta-- {
		month := base.Month() + time.Month(delta)
		year := base.Year()
		for month < time.January {
			month += 12
			year--
		}
		cand := AnchorInstant(anchorDay, loc, year, month)
		if !cand.Before(base) {
			continue
		}
		if best.IsZero() || cand.After(best) {
			best = cand
		}
	}
	if best.IsZero() {
		return base.AddDate(0, -1, 0)
	}
	return best
}

// WindowBefore returns the cycle immediately preceding the given boundary.
func WindowBefore(boundary time.Time, anchorDay int, loc *time.Location) Boundary {
	start := PrevBoundary(boundary, anchorDay, loc)
	return Boundary{Start: start, End: boundary.UTC()}
}

// CandidateWindows returns every cycle that can overlap [from, to). The first
// element is the cycle containing `to` (normally the current cycle) and older
// cycles are appended while they still overlap the interval.
func CandidateWindows(anchorDay int, loc *time.Location, from, to time.Time) []Boundary {
	if !to.After(from) {
		return []Boundary{Containing(anchorDay, loc, to)}
	}
	cur := Containing(anchorDay, loc, to)
	out := []Boundary{cur}
	for i := 0; i < 400 && cur.Start.After(from); i++ {
		prev := WindowBefore(cur.Start, anchorDay, loc)
		out = append(out, prev)
		cur = prev
	}
	return out
}

// Window returns the cycle starting at start.
func Window(start time.Time, anchorDay int, loc *time.Location) Boundary {
	return Boundary{Start: start.UTC(), End: NextBoundary(start, anchorDay, loc)}
}

// AllocateSample splits a sampling interval across the supplied candidate
// cycles by overlap duration. The integer remainder is attributed to the last
// overlapping slice, matching product.md §8.5 (estimated + boundary_split).
func AllocateSample(sampleStart, sampleEnd time.Time, periods []Boundary, up, down int64) []Allocation {
	if !sampleEnd.After(sampleStart) || (up == 0 && down == 0) {
		return nil
	}
	total := sampleEnd.Sub(sampleStart)
	type overlap struct {
		period Boundary
		dur    time.Duration
	}
	var overlaps []overlap
	for _, p := range periods {
		if p.End.Before(sampleEnd) || !p.Start.Before(p.End) {
			// keep analysis simple below; no early exit
		}
		s := p.Start
		if sampleStart.After(s) {
			s = sampleStart
		}
		e := p.End
		if sampleEnd.Before(e) {
			e = sampleEnd
		}
		if e.After(s) {
			overlaps = append(overlaps, overlap{period: p, dur: e.Sub(s)})
		}
	}
	if len(overlaps) == 0 {
		return nil
	}
	// Chronological order: the integer remainder always lands on the latest
	// overlapping slice regardless of the caller's window order.
	sort.Slice(overlaps, func(i, j int) bool { return overlaps[i].period.Start.Before(overlaps[j].period.Start) })
	out := make([]Allocation, 0, len(overlaps))
	var sumUp, sumDown int64
	for i, o := range overlaps {
		var sliceUp, sliceDown int64
		if i == len(overlaps)-1 {
			sliceUp = up - sumUp
			sliceDown = down - sumDown
		} else {
			sliceUp = mulDiv(up, o.dur, total)
			sliceDown = mulDiv(down, o.dur, total)
			sumUp += sliceUp
			sumDown += sliceDown
		}
		if sliceUp == 0 && sliceDown == 0 {
			continue
		}
		out = append(out, Allocation{
			Period:    o.period,
			UpBytes:   sliceUp,
			DownBytes: sliceDown,
			Estimated: len(overlaps) > 1,
			Split:     len(overlaps) > 1,
		})
	}
	return out
}

// mulDiv returns value*part/whole using exact integer arithmetic.
func mulDiv(value int64, part, whole time.Duration) int64 {
	if value == 0 || whole <= 0 {
		return 0
	}
	num := new(big.Int).Mul(big.NewInt(value), big.NewInt(int64(part)))
	num.Quo(num, big.NewInt(int64(whole)))
	return num.Int64()
}
