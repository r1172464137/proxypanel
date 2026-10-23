package period

import (
	"testing"
	"time"
)

func mustLoc(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := LoadTimezone(name)
	if err != nil {
		t.Fatalf("LoadTimezone(%q): %v", name, err)
	}
	return loc
}

func ts(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return v.UTC()
}

func TestAnchorInstantAsiaShanghai(t *testing.T) {
	loc := mustLoc(t, "Asia/Shanghai") // UTC+8, no DST
	// anchor 1: local 2026-11-01 00:00 == 2026-10-31T16:00Z
	if got := AnchorInstant(1, loc, 2026, time.November); !got.Equal(ts(t, "2026-10-31T16:00:00Z")) {
		t.Fatalf("anchor 1 Nov 2026 = %s", got)
	}
	// anchor 15: local 2026-03-15 00:00 == 2026-03-14T16:00Z
	if got := AnchorInstant(15, loc, 2026, time.March); !got.Equal(ts(t, "2026-03-14T16:00:00Z")) {
		t.Fatalf("anchor 15 Mar 2026 = %s", got)
	}
}

func TestAnchorInstantClipsShortMonths(t *testing.T) {
	loc := mustLoc(t, "Asia/Shanghai")
	// 2026 is not a leap year: anchor 31 -> 2026-02-28 local 00:00
	if got := AnchorInstant(31, loc, 2026, time.February); !got.Equal(ts(t, "2026-02-27T16:00:00Z")) {
		t.Fatalf("anchor 31 Feb 2026 = %s", got)
	}
	// 2028 is a leap year: anchor 31 -> 2028-02-29 local 00:00
	if got := AnchorInstant(31, loc, 2028, time.February); !got.Equal(ts(t, "2028-02-28T16:00:00Z")) {
		t.Fatalf("anchor 31 Feb 2028 = %s", got)
	}
	if d := ClampDay(31, 2026, time.April); d != 30 {
		t.Fatalf("clamp April = %d", d)
	}
}

func TestContainingAndNextBoundary(t *testing.T) {
	loc := mustLoc(t, "Asia/Shanghai")
	now := ts(t, "2026-10-20T06:00:00Z")
	w := Containing(1, loc, now)
	if !w.Start.Equal(ts(t, "2026-09-30T16:00:00Z")) || !w.End.Equal(ts(t, "2026-10-31T16:00:00Z")) {
		t.Fatalf("containing anchor 1 = %+v", w)
	}
	// product.md §8.3 example: current 2026-03-15..2026-04-15 (anchor 15),
	// then anchor 31 -> next cycle 2026-04-15..2026-04-30 local.
	cur := Window(ts(t, "2026-03-14T16:00:00Z"), 15, loc)
	if !cur.End.Equal(ts(t, "2026-04-14T16:00:00Z")) {
		t.Fatalf("current end = %s", cur.End)
	}
	next := Window(cur.End, 31, loc)
	if !next.Start.Equal(cur.End) || !next.End.Equal(ts(t, "2026-04-29T16:00:00Z")) {
		t.Fatalf("next window under anchor 31 = %+v", next)
	}
}

func TestNextBoundaryIsStrictlyIncreasing(t *testing.T) {
	for _, tz := range []string{"Asia/Shanghai", "UTC", "America/Havana", "America/Santiago", "Australia/Lord_Howe"} {
		loc := mustLoc(t, tz)
		for _, anchor := range []int{1, 15, 28, 29, 30, 31} {
			cur := AnchorInstant(anchor, loc, 2026, time.January)
			for i := 0; i < 24; i++ {
				nxt := NextBoundary(cur, anchor, loc)
				if !nxt.After(cur) {
					t.Fatalf("%s anchor %d not increasing: %s -> %s", tz, anchor, cur, nxt)
				}
				span := nxt.Sub(cur)
				if span < 25*24*time.Hour || span > 32*24*time.Hour {
					t.Fatalf("%s anchor %d implausible cycle length %v", tz, anchor, span)
				}
				cur = nxt
			}
		}
	}
}

func TestDSTGapTakesFirstValidInstant(t *testing.T) {
	loc := mustLoc(t, "America/Havana")
	// Havana springs forward at 2026-03-08 00:00 -> 01:00, so local midnight
	// does not exist. The first valid instant is 01:00 local (UTC-4) = 05:00Z.
	got := AnchorInstant(8, loc, 2026, time.March)
	if !got.Equal(ts(t, "2026-03-08T05:00:00Z")) {
		t.Fatalf("gap resolution = %s", got)
	}
	local := got.In(loc)
	if h, m, _ := local.Clock(); h != 1 || m != 0 {
		t.Fatalf("gap resolution local time = %s", local)
	}
}

func TestDSTAmbiguousTakesEarlierInstant(t *testing.T) {
	loc := mustLoc(t, "America/Havana")
	// Havana falls back at 2026-11-01 01:00 -> 00:00, so local midnight happens
	// twice; the earlier absolute instant is 2026-11-01T04:00Z (UTC-4).
	got := AnchorInstant(1, loc, 2026, time.November)
	if !got.Equal(ts(t, "2026-11-01T04:00:00Z")) {
		t.Fatalf("ambiguous resolution = %s", got)
	}
}

func TestAllocateSampleAcrossBoundary(t *testing.T) {
	b := Boundary{Start: ts(t, "2026-10-01T00:00:00Z"), End: ts(t, "2026-11-01T00:00:00Z")}
	next := Boundary{Start: b.End, End: ts(t, "2026-12-01T00:00:00Z")}
	// 60 minute sample starting 40 minutes before the boundary.
	start := b.End.Add(-40 * time.Minute)
	end := b.End.Add(20 * time.Minute)
	got := AllocateSample(start, end, []Boundary{b, next}, 300, 600)
	if len(got) != 2 {
		t.Fatalf("expected 2 slices, got %+v", got)
	}
	if got[0].UpBytes != 200 || got[0].DownBytes != 400 {
		t.Fatalf("first slice = %+v", got[0])
	}
	if got[1].UpBytes != 100 || got[1].DownBytes != 200 {
		t.Fatalf("last slice takes the remainder = %+v", got[1])
	}
	if !got[0].Estimated || !got[0].Split || !got[1].Estimated {
		t.Fatalf("split slices must be flagged estimated/boundary_split: %+v", got)
	}
}

func TestAllocateSampleSinglePeriodIsExact(t *testing.T) {
	b := Boundary{Start: ts(t, "2026-10-01T00:00:00Z"), End: ts(t, "2026-11-01T00:00:00Z")}
	got := AllocateSample(ts(t, "2026-10-02T00:00:00Z"), ts(t, "2026-10-02T00:05:00Z"), []Boundary{b}, 11, 22)
	if len(got) != 1 {
		t.Fatalf("expected single slice, got %+v", got)
	}
	if got[0].UpBytes != 11 || got[0].DownBytes != 22 || got[0].Estimated || got[0].Split {
		t.Fatalf("single period must be exact and not estimated: %+v", got[0])
	}
}

func TestAllocateSampleRemainderGoesToLast(t *testing.T) {
	p1 := Boundary{Start: ts(t, "2026-10-01T00:00:00Z"), End: ts(t, "2026-10-01T00:10:00Z")}
	p2 := Boundary{Start: p1.End, End: ts(t, "2026-10-01T00:20:00Z")}
	p3 := Boundary{Start: p2.End, End: ts(t, "2026-10-01T00:30:00Z")}
	got := AllocateSample(p1.Start, p3.End, []Boundary{p1, p2, p3}, 100, 0)
	if len(got) != 3 {
		t.Fatalf("expected 3 slices, got %+v", got)
	}
	var sum int64
	for _, a := range got {
		sum += a.UpBytes
	}
	if sum != 100 {
		t.Fatalf("slices must sum to the reported bytes, got %d", sum)
	}
	if got[0].UpBytes != 33 || got[1].UpBytes != 33 || got[2].UpBytes != 34 {
		t.Fatalf("remainder to last segment: %+v", got)
	}
}

func TestAllocateSampleZeroLengthIsDropped(t *testing.T) {
	b := Boundary{Start: ts(t, "2026-10-01T00:00:00Z"), End: ts(t, "2026-11-01T00:00:00Z")}
	if got := AllocateSample(ts(t, "2026-10-02T00:00:00Z"), ts(t, "2026-10-02T00:00:00Z"), []Boundary{b}, 1, 1); got != nil {
		t.Fatalf("zero length sample must be dropped: %+v", got)
	}
}

func TestLoadTimezoneRejectsEmptyAndUnknown(t *testing.T) {
	if _, err := LoadTimezone(""); err == nil {
		t.Fatal("empty timezone must fail")
	}
	if _, err := LoadTimezone("Mars/Olympus"); err == nil {
		t.Fatal("unknown timezone must fail")
	}
}

func TestContainingUsesLocalCalendarMonth(t *testing.T) {
	for _, tc := range []struct {
		name, zone, at, start, end string
	}{
		{"east-next-month", "Asia/Shanghai", "2026-10-31T17:00:00Z", "2026-10-31T16:00:00Z", "2026-11-30T16:00:00Z"},
		{"east-next-year", "Pacific/Kiritimati", "2026-12-31T11:00:00Z", "2026-12-31T10:00:00Z", "2027-01-31T10:00:00Z"},
		{"west-previous-month", "America/New_York", "2026-11-01T02:00:00Z", "2026-10-01T04:00:00Z", "2026-11-01T04:00:00Z"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			at := ts(t, tc.at)
			got := Containing(1, mustLoc(t, tc.zone), at)
			if !got.Start.Equal(ts(t, tc.start)) || !got.End.Equal(ts(t, tc.end)) {
				t.Fatalf("containing cycle = %+v, want [%s,%s)", got, tc.start, tc.end)
			}
			if at.Before(got.Start) || !at.Before(got.End) {
				t.Fatalf("cycle does not contain requested instant: %+v at %s", got, at)
			}
		})
	}
}

func TestPrevBoundaryUsesLocalCalendarMonth(t *testing.T) {
	for _, tc := range []struct {
		name, zone, at, want string
	}{
		{"after-anchor", "Asia/Shanghai", "2026-10-31T17:00:00Z", "2026-10-31T16:00:00Z"},
		{"exact-anchor-is-exclusive", "Asia/Shanghai", "2026-10-31T16:00:00Z", "2026-09-30T16:00:00Z"},
		{"next-year", "Pacific/Kiritimati", "2026-12-31T11:00:00Z", "2026-12-31T10:00:00Z"},
		{"west-previous-month", "America/New_York", "2026-11-01T02:00:00Z", "2026-10-01T04:00:00Z"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := PrevBoundary(ts(t, tc.at), 1, mustLoc(t, tc.zone)); !got.Equal(ts(t, tc.want)) {
				t.Fatalf("previous boundary = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestDSTGapAtLargeOffsetAndSkippedDate(t *testing.T) {
	for _, tc := range []struct {
		zone  string
		year  int
		month time.Month
		day   int
		want  string
	}{
		{"Asia/Tehran", 2021, time.March, 22, "2021-03-21T20:30:00Z"},
		{"Pacific/Apia", 2011, time.December, 30, "2011-12-30T10:00:00Z"},
	} {
		t.Run(tc.zone, func(t *testing.T) {
			if got := AnchorInstant(tc.day, mustLoc(t, tc.zone), tc.year, tc.month); !got.Equal(ts(t, tc.want)) {
				t.Fatalf("first valid instant = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestLoadTimezoneRejectsHostDependentLocal(t *testing.T) {
	if loc, err := LoadTimezone("Local"); err == nil {
		t.Fatalf("host-dependent timezone was accepted: %v", loc)
	}
}

func TestPrevBoundaryAndCandidateWindows(t *testing.T) {
	loc := mustLoc(t, "Asia/Shanghai")
	now := ts(t, "2026-10-20T06:00:00Z") // cycle 2026-09-30T16:00Z .. 2026-10-31T16:00Z
	prev := PrevBoundary(ts(t, "2026-09-30T16:00:00Z"), 1, loc)
	if !prev.Equal(ts(t, "2026-08-31T16:00:00Z")) {
		t.Fatalf("prev boundary = %s", prev)
	}
	// sample starting two cycles back must be covered by three candidate windows
	windows := CandidateWindows(1, loc, ts(t, "2026-08-20T00:00:00Z"), now)
	if len(windows) != 3 {
		t.Fatalf("windows = %+v", windows)
	}
	if !windows[0].Start.Equal(ts(t, "2026-09-30T16:00:00Z")) {
		t.Fatalf("first window = %+v", windows[0])
	}
	if !windows[2].Start.Equal(ts(t, "2026-07-31T16:00:00Z")) {
		t.Fatalf("oldest window = %+v", windows[2])
	}
}
