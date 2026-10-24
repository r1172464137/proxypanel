# CPREV-005 frozen input

3435f1d..fe4bf8e; no WIP.
```diff
diff --git a/internal/panel/period/period.go b/internal/panel/period/period.go
index bb396d8..0f268db 100644
--- a/internal/panel/period/period.go
+++ b/internal/panel/period/period.go
@@ -37,6 +37,9 @@ func LoadTimezone(name string) (*time.Location, error) {
 	if name == "" {
 		return nil, fmt.Errorf("timezone is required")
 	}
+	if name == "Local" {
+		return nil, fmt.Errorf("explicit IANA timezone is required; Local depends on the host")
+	}
 	loc, err := time.LoadLocation(name)
 	if err != nil {
 		return nil, fmt.Errorf("unknown timezone %q", name)
@@ -75,51 +78,38 @@ func AnchorInstant(anchorDay int, loc *time.Location, year int, month time.Month
 // resolveWallClock maps a local wall clock date at 00:00 to a UTC instant.
 func resolveWallClock(year int, month time.Month, day int, loc *time.Location) time.Time {
 	wall := time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
-	_, offA := wall.Add(-36 * time.Hour).In(loc).Zone()
-	_, offB := wall.Add(36 * time.Hour).In(loc).Zone()
-	var best time.Time
-	seen := map[int]bool{}
-	for _, off := range []int{offA, offB} {
-		if seen[off] {
-			continue
-		}
-		seen[off] = true
-		inst := wall.Add(-time.Duration(off) * time.Second)
-		if sameWallClock(inst.In(loc), year, month, day) {
-			if best.IsZero() || inst.Before(best) {
-				best = inst
+	_, offset := wall.In(loc).Zone()
+	guess := wall.Add(-time.Duration(offset) * time.Second)
+	// Two days on either side cover IANA date-line skips as well as DST gaps.
+	cursor, limit := guess.Add(-48*time.Hour), guess.Add(48*time.Hour)
+	var exact, afterGap time.Time
+	var hasExact, hasGap bool
+	for !cursor.After(limit) {
+		local := cursor.In(loc)
+		_, offset := local.Zone()
+		_, end := local.ZoneBounds()
+		candidate := wall.Add(-time.Duration(offset) * time.Second)
+		// The local clock is linear only within one constant-offset interval.
+		if !candidate.Before(cursor) && (end.IsZero() || candidate.Before(end)) {
+			if !hasExact || candidate.Before(exact) {
+				exact, hasExact = candidate, true
 			}
+		} else if candidate.Before(cursor) && (!hasGap || cursor.Before(afterGap)) {
+			afterGap, hasGap = cursor, true
 		}
+		if end.IsZero() || !end.After(cursor) {
+			break
+		}
+		cursor = end
 	}
-	if !best.IsZero() {
-		return best.UTC()
+	// Prefer an actual midnight; repeated midnights use the earlier instant.
+	if hasExact {
+		return exact.UTC()
 	}
-	// DST gap: the wall clock never occurred. Take the first instant whose local
-	// wall clock is not before the requested one.
-	start := wall.Add(-3 * time.Hour)
-	for i := 0; i <= 9*60; i++ {
-		inst := start.Add(time.Duration(i) * time.Minute)
-		local := inst.In(loc)
-		ly, lm, ld := local.Date()
-		if ly < year || (ly == year && lm < month) || (ly == year && lm == month && ld < day) {
-			continue
-		}
-		if ly == year && lm == month && ld == day {
-			return inst.UTC()
-		}
-		// Crossed into a later date without ever observing this date: the gap
-		// swallowed the whole day, keep the first instant of the next date.
-		if ly > year || lm > month || ld > day {
-			return inst.UTC()
-		}
+	if hasGap {
+		return afterGap.UTC()
 	}
-	return wall.UTC()
-}
-
-func sameWallClock(t time.Time, year int, month time.Month, day int) bool {
-	y, m, d := t.Date()
-	h, min, sec := t.Clock()
-	return y == year && m == month && d == day && h == 0 && min == 0 && sec == 0
+	return guess.UTC()
 }
 
 // NextBoundary returns the first anchor instant strictly after `after`.
@@ -161,7 +151,7 @@ func Containing(anchorDay int, loc *time.Location, t time.Time) Boundary {
 		loc = time.UTC
 	}
 	at := t.UTC()
-	base := at
+	base := at.In(loc)
 	var start time.Time
 	for delta := 0; delta >= -2; delta-- {
 		month := base.Month() + time.Month(delta)
@@ -178,7 +168,7 @@ func Containing(anchorDay int, loc *time.Location, t time.Time) Boundary {
 		}
 	}
 	if start.IsZero() {
-		start = AnchorInstant(anchorDay, loc, at.Year(), at.Month())
+		start = AnchorInstant(anchorDay, loc, base.Year(), base.Month())
 	}
 	return Boundary{Start: start, End: NextBoundary(start, anchorDay, loc)}
 }
@@ -188,7 +178,7 @@ func PrevBoundary(before time.Time, anchorDay int, loc *time.Location) time.Time
 	if loc == nil {
 		loc = time.UTC
 	}
-	base := before.UTC()
+	base := before.In(loc)
 	var best time.Time
 	for delta := 0; delta >= -3; delta-- {
 		month := base.Month() + time.Month(delta)
@@ -239,6 +229,16 @@ func Window(start time.Time, anchorDay int, loc *time.Location) Boundary {
 	return Boundary{Start: start.UTC(), End: NextBoundary(start, anchorDay, loc)}
 }
 
+// Transition preserves oldEnd as the first new-rule cycle's start, then rolls
+// forward into the calendar cycles aligned with the new rule.
+func Transition(oldEnd time.Time, anchorDay int, loc *time.Location, now time.Time) Boundary {
+	first := Window(oldEnd, anchorDay, loc)
+	if now.Before(first.End) {
+		return first
+	}
+	return Containing(anchorDay, loc, now)
+}
+
 // AllocateSample splits a sampling interval across the supplied candidate
 // cycles by overlap duration. The integer remainder is attributed to the last
 // overlapping slice, matching product.md §8.5 (estimated + boundary_split).
@@ -310,5 +310,3 @@ func mulDiv(value int64, part, whole time.Duration) int64 {
 	num.Quo(num, big.NewInt(int64(whole)))
 	return num.Int64()
 }
-
-
diff --git a/internal/panel/period/period_test.go b/internal/panel/period/period_test.go
index 9e2f59f..f4774bd 100644
--- a/internal/panel/period/period_test.go
+++ b/internal/panel/period/period_test.go
@@ -179,7 +179,70 @@ func TestLoadTimezoneRejectsEmptyAndUnknown(t *testing.T) {
 	if _, err := LoadTimezone("Mars/Olympus"); err == nil {
 		t.Fatal("unknown timezone must fail")
 	}
-}
+}
+
+func TestContainingUsesLocalCalendarMonth(t *testing.T) {
+	for _, tc := range []struct {
+		name, zone, at, start, end string
+	}{
+		{"east-next-month", "Asia/Shanghai", "2026-10-31T17:00:00Z", "2026-10-31T16:00:00Z", "2026-11-30T16:00:00Z"},
+		{"east-next-year", "Pacific/Kiritimati", "2026-12-31T11:00:00Z", "2026-12-31T10:00:00Z", "2027-01-31T10:00:00Z"},
+		{"west-previous-month", "America/New_York", "2026-11-01T02:00:00Z", "2026-10-01T04:00:00Z", "2026-11-01T04:00:00Z"},
+	} {
+		t.Run(tc.name, func(t *testing.T) {
+			at := ts(t, tc.at)
+			got := Containing(1, mustLoc(t, tc.zone), at)
+			if !got.Start.Equal(ts(t, tc.start)) || !got.End.Equal(ts(t, tc.end)) {
+				t.Fatalf("containing cycle = %+v, want [%s,%s)", got, tc.start, tc.end)
+			}
+			if at.Before(got.Start) || !at.Before(got.End) {
+				t.Fatalf("cycle does not contain requested instant: %+v at %s", got, at)
+			}
+		})
+	}
+}
+
+func TestPrevBoundaryUsesLocalCalendarMonth(t *testing.T) {
+	for _, tc := range []struct {
+		name, zone, at, want string
+	}{
+		{"after-anchor", "Asia/Shanghai", "2026-10-31T17:00:00Z", "2026-10-31T16:00:00Z"},
+		{"exact-anchor-is-exclusive", "Asia/Shanghai", "2026-10-31T16:00:00Z", "2026-09-30T16:00:00Z"},
+		{"next-year", "Pacific/Kiritimati", "2026-12-31T11:00:00Z", "2026-12-31T10:00:00Z"},
+		{"west-previous-month", "America/New_York", "2026-11-01T02:00:00Z", "2026-10-01T04:00:00Z"},
+	} {
+		t.Run(tc.name, func(t *testing.T) {
+			if got := PrevBoundary(ts(t, tc.at), 1, mustLoc(t, tc.zone)); !got.Equal(ts(t, tc.want)) {
+				t.Fatalf("previous boundary = %s, want %s", got, tc.want)
+			}
+		})
+	}
+}
+
+func TestDSTGapAtLargeOffsetAndSkippedDate(t *testing.T) {
+	for _, tc := range []struct {
+		zone  string
+		year  int
+		month time.Month
+		day   int
+		want  string
+	}{
+		{"Asia/Tehran", 2021, time.March, 22, "2021-03-21T20:30:00Z"},
+		{"Pacific/Apia", 2011, time.December, 30, "2011-12-30T10:00:00Z"},
+	} {
+		t.Run(tc.zone, func(t *testing.T) {
+			if got := AnchorInstant(tc.day, mustLoc(t, tc.zone), tc.year, tc.month); !got.Equal(ts(t, tc.want)) {
+				t.Fatalf("first valid instant = %s, want %s", got, tc.want)
+			}
+		})
+	}
+}
+
+func TestLoadTimezoneRejectsHostDependentLocal(t *testing.T) {
+	if loc, err := LoadTimezone("Local"); err == nil {
+		t.Fatalf("host-dependent timezone was accepted: %v", loc)
+	}
+}
 
 func TestPrevBoundaryAndCandidateWindows(t *testing.T) {
 	loc := mustLoc(t, "Asia/Shanghai")
@@ -199,4 +262,4 @@ func TestPrevBoundaryAndCandidateWindows(t *testing.T) {
 	if !windows[2].Start.Equal(ts(t, "2026-07-31T16:00:00Z")) {
 		t.Fatalf("oldest window = %+v", windows[2])
 	}
-}
+}

```

## internal/panel/period/period.go
```text
1: // Package period implements the frozen monthly billing window algorithm
2: // (period/v1): a cycle starts at local midnight of anchor_day in the business
3: // timezone, clipped to the last day of short months, with deterministic DST
4: // handling. The same implementation feeds the panel authorization, subscription
5: // output and the agent snapshot.
6: package period
7: 
8: import (
9: 	"fmt"
10: 	"math/big"
11: 	"sort"
12: 	"time"
13: 	_ "time/tzdata" // embed IANA data so LoadLocation works on any host
14: )
15: 
16: // AlgorithmVersion identifies the cycle algorithm on the wire.
17: const AlgorithmVersion = "period/v1"
18: 
19: // Boundary is a half open cycle window [Start, End).
20: type Boundary struct {
21: 	Start time.Time
22: 	End   time.Time
23: }
24: 
25: // Allocation is one ledger slice for a sample that may straddle boundaries.
26: type Allocation struct {
27: 	Period    Boundary
28: 	UpBytes   int64
29: 	DownBytes int64
30: 	Estimated bool
31: 	Split     bool
32: }
33: 
34: // LoadTimezone resolves an explicit IANA name. Empty or unknown names are an
35: // error: the panel never infers a timezone from the host or the browser.
36: func LoadTimezone(name string) (*time.Location, error) {
37: 	if name == "" {
38: 		return nil, fmt.Errorf("timezone is required")
39: 	}
40: 	if name == "Local" {
41: 		return nil, fmt.Errorf("explicit IANA timezone is required; Local depends on the host")
42: 	}
43: 	loc, err := time.LoadLocation(name)
44: 	if err != nil {
45: 		return nil, fmt.Errorf("unknown timezone %q", name)
46: 	}
47: 	return loc, nil
48: }
49: 
50: // DaysIn returns the number of days in the month.
51: func DaysIn(year int, month time.Month) int {
52: 	return time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC).Day()
53: }
54: 
55: // ClampDay clips anchor_day to the number of days in the given month, which is
56: // what makes anchor_day=31 land on 28/29 February without special cases.
57: func ClampDay(anchorDay int, year int, month time.Month) int {
58: 	if anchorDay < 1 {
59: 		anchorDay = 1
60: 	}
61: 	if max := DaysIn(year, month); anchorDay > max {
62: 		return max
63: 	}
64: 	return anchorDay
65: }
66: 
67: // AnchorInstant returns the UTC instant of local midnight on the clipped
68: // anchor day. If midnight does not exist (DST spring forward) the first valid
69: // instant after the gap is used; if midnight happens twice (DST fall back) the
70: // earlier instant wins.
71: func AnchorInstant(anchorDay int, loc *time.Location, year int, month time.Month) time.Time {
72: 	if loc == nil {
73: 		loc = time.UTC
74: 	}
75: 	return resolveWallClock(year, month, ClampDay(anchorDay, year, month), loc)
76: }
77: 
78: // resolveWallClock maps a local wall clock date at 00:00 to a UTC instant.
79: func resolveWallClock(year int, month time.Month, day int, loc *time.Location) time.Time {
80: 	wall := time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
81: 	_, offset := wall.In(loc).Zone()
82: 	guess := wall.Add(-time.Duration(offset) * time.Second)
83: 	// Two days on either side cover IANA date-line skips as well as DST gaps.
84: 	cursor, limit := guess.Add(-48*time.Hour), guess.Add(48*time.Hour)
85: 	var exact, afterGap time.Time
86: 	var hasExact, hasGap bool
87: 	for !cursor.After(limit) {
88: 		local := cursor.In(loc)
89: 		_, offset := local.Zone()
90: 		_, end := local.ZoneBounds()
91: 		candidate := wall.Add(-time.Duration(offset) * time.Second)
92: 		// The local clock is linear only within one constant-offset interval.
93: 		if !candidate.Before(cursor) && (end.IsZero() || candidate.Before(end)) {
94: 			if !hasExact || candidate.Before(exact) {
95: 				exact, hasExact = candidate, true
96: 			}
97: 		} else if candidate.Before(cursor) && (!hasGap || cursor.Before(afterGap)) {
98: 			afterGap, hasGap = cursor, true
99: 		}
100: 		if end.IsZero() || !end.After(cursor) {
101: 			break
102: 		}
103: 		cursor = end
104: 	}
105: 	// Prefer an actual midnight; repeated midnights use the earlier instant.
106: 	if hasExact {
107: 		return exact.UTC()
108: 	}
109: 	if hasGap {
110: 		return afterGap.UTC()
111: 	}
112: 	return guess.UTC()
113: }
114: 
115: // NextBoundary returns the first anchor instant strictly after `after`.
116: func NextBoundary(after time.Time, anchorDay int, loc *time.Location) time.Time {
117: 	if loc == nil {
118: 		loc = time.UTC
119: 	}
120: 	base := after.UTC()
121: 	var best time.Time
122: 	for delta := -1; delta <= 3; delta++ {
123: 		month := base.Month() + time.Month(delta)
124: 		year := base.Year()
125: 		for month < time.January {
126: 			month += 12
127: 			year--
128: 		}
129: 		for month > time.December {
130: 			month -= 12
131: 			year++
132: 		}
133: 		cand := AnchorInstant(anchorDay, loc, year, month)
134: 		if !cand.After(base) {
135: 			continue
136: 		}
137: 		if best.IsZero() || cand.Before(best) {
138: 			best = cand
139: 		}
140: 	}
141: 	if best.IsZero() {
142: 		// Fall back to a month later; unreachable for sane anchor days.
143: 		return base.AddDate(0, 1, 0)
144: 	}
145: 	return best
146: }
147: 
148: // Containing returns the cycle window that contains t.
149: func Containing(anchorDay int, loc *time.Location, t time.Time) Boundary {
150: 	if loc == nil {
151: 		loc = time.UTC
152: 	}
153: 	at := t.UTC()
154: 	base := at.In(loc)
155: 	var start time.Time
156: 	for delta := 0; delta >= -2; delta-- {
157: 		month := base.Month() + time.Month(delta)
158: 		year := base.Year()
159: 		for month < time.January {
160: 			month += 12
161: 			year--
162: 		}
163: 		cand := AnchorInstant(anchorDay, loc, year, month)
164: 		if !cand.After(at) {
165: 			if start.IsZero() || cand.After(start) {
166: 				start = cand
167: 			}
168: 		}
169: 	}
170: 	if start.IsZero() {
171: 		start = AnchorInstant(anchorDay, loc, base.Year(), base.Month())
172: 	}
173: 	return Boundary{Start: start, End: NextBoundary(start, anchorDay, loc)}
174: }
175: 
176: // PrevBoundary returns the last anchor instant strictly before `before`.
177: func PrevBoundary(before time.Time, anchorDay int, loc *time.Location) time.Time {
178: 	if loc == nil {
179: 		loc = time.UTC
180: 	}
181: 	base := before.In(loc)
182: 	var best time.Time
183: 	for delta := 0; delta >= -3; delta-- {
184: 		month := base.Month() + time.Month(delta)
185: 		year := base.Year()
186: 		for month < time.January {
187: 			month += 12
188: 			year--
189: 		}
190: 		cand := AnchorInstant(anchorDay, loc, year, month)
191: 		if !cand.Before(base) {
192: 			continue
193: 		}
194: 		if best.IsZero() || cand.After(best) {
195: 			best = cand
196: 		}
197: 	}
198: 	if best.IsZero() {
199: 		return base.AddDate(0, -1, 0)
200: 	}
201: 	return best
202: }
203: 
204: // WindowBefore returns the cycle immediately preceding the given boundary.
205: func WindowBefore(boundary time.Time, anchorDay int, loc *time.Location) Boundary {
206: 	start := PrevBoundary(boundary, anchorDay, loc)
207: 	return Boundary{Start: start, End: boundary.UTC()}
208: }
209: 
210: // CandidateWindows returns every cycle that can overlap [from, to). The first
211: // element is the cycle containing `to` (normally the current cycle) and older
212: // cycles are appended while they still overlap the interval.
213: func CandidateWindows(anchorDay int, loc *time.Location, from, to time.Time) []Boundary {
214: 	if !to.After(from) {
215: 		return []Boundary{Containing(anchorDay, loc, to)}
216: 	}
217: 	cur := Containing(anchorDay, loc, to)
218: 	out := []Boundary{cur}
219: 	for i := 0; i < 400 && cur.Start.After(from); i++ {
220: 		prev := WindowBefore(cur.Start, anchorDay, loc)
221: 		out = append(out, prev)
222: 		cur = prev
223: 	}
224: 	return out
225: }
226: 
227: // Window returns the cycle starting at start.
228: func Window(start time.Time, anchorDay int, loc *time.Location) Boundary {
229: 	return Boundary{Start: start.UTC(), End: NextBoundary(start, anchorDay, loc)}
230: }
231: 
232: // Transition preserves oldEnd as the first new-rule cycle's start, then rolls
233: // forward into the calendar cycles aligned with the new rule.
234: func Transition(oldEnd time.Time, anchorDay int, loc *time.Location, now time.Time) Boundary {
235: 	first := Window(oldEnd, anchorDay, loc)
236: 	if now.Before(first.End) {
237: 		return first
238: 	}
239: 	return Containing(anchorDay, loc, now)
240: }
241: 
242: // AllocateSample splits a sampling interval across the supplied candidate
243: // cycles by overlap duration. The integer remainder is attributed to the last
244: // overlapping slice, matching product.md §8.5 (estimated + boundary_split).
245: func AllocateSample(sampleStart, sampleEnd time.Time, periods []Boundary, up, down int64) []Allocation {
246: 	if !sampleEnd.After(sampleStart) || (up == 0 && down == 0) {
247: 		return nil
248: 	}
249: 	total := sampleEnd.Sub(sampleStart)
250: 	type overlap struct {
251: 		period Boundary
252: 		dur    time.Duration
253: 	}
254: 	var overlaps []overlap
255: 	for _, p := range periods {
256: 		if p.End.Before(sampleEnd) || !p.Start.Before(p.End) {
257: 			// keep analysis simple below; no early exit
258: 		}
259: 		s := p.Start
260: 		if sampleStart.After(s) {
261: 			s = sampleStart
262: 		}
263: 		e := p.End
264: 		if sampleEnd.Before(e) {
265: 			e = sampleEnd
266: 		}
267: 		if e.After(s) {
268: 			overlaps = append(overlaps, overlap{period: p, dur: e.Sub(s)})
269: 		}
270: 	}
271: 	if len(overlaps) == 0 {
272: 		return nil
273: 	}
274: 	// Chronological order: the integer remainder always lands on the latest
275: 	// overlapping slice regardless of the caller's window order.
276: 	sort.Slice(overlaps, func(i, j int) bool { return overlaps[i].period.Start.Before(overlaps[j].period.Start) })
277: 	out := make([]Allocation, 0, len(overlaps))
278: 	var sumUp, sumDown int64
279: 	for i, o := range overlaps {
280: 		var sliceUp, sliceDown int64
281: 		if i == len(overlaps)-1 {
282: 			sliceUp = up - sumUp
283: 			sliceDown = down - sumDown
284: 		} else {
285: 			sliceUp = mulDiv(up, o.dur, total)
286: 			sliceDown = mulDiv(down, o.dur, total)
287: 			sumUp += sliceUp
288: 			sumDown += sliceDown
289: 		}
290: 		if sliceUp == 0 && sliceDown == 0 {
291: 			continue
292: 		}
293: 		out = append(out, Allocation{
294: 			Period:    o.period,
295: 			UpBytes:   sliceUp,
296: 			DownBytes: sliceDown,
297: 			Estimated: len(overlaps) > 1,
298: 			Split:     len(overlaps) > 1,
299: 		})
300: 	}
301: 	return out
302: }
303: 
304: // mulDiv returns value*part/whole using exact integer arithmetic.
305: func mulDiv(value int64, part, whole time.Duration) int64 {
306: 	if value == 0 || whole <= 0 {
307: 		return 0
308: 	}
309: 	num := new(big.Int).Mul(big.NewInt(value), big.NewInt(int64(part)))
310: 	num.Quo(num, big.NewInt(int64(whole)))
311: 	return num.Int64()
312: }
```

## internal/panel/period/period_test.go
```text
1: package period
2: 
3: import (
4: 	"testing"
5: 	"time"
6: )
7: 
8: func mustLoc(t *testing.T, name string) *time.Location {
9: 	t.Helper()
10: 	loc, err := LoadTimezone(name)
11: 	if err != nil {
12: 		t.Fatalf("LoadTimezone(%q): %v", name, err)
13: 	}
14: 	return loc
15: }
16: 
17: func ts(t *testing.T, s string) time.Time {
18: 	t.Helper()
19: 	v, err := time.Parse(time.RFC3339, s)
20: 	if err != nil {
21: 		t.Fatalf("parse %q: %v", s, err)
22: 	}
23: 	return v.UTC()
24: }
25: 
26: func TestAnchorInstantAsiaShanghai(t *testing.T) {
27: 	loc := mustLoc(t, "Asia/Shanghai") // UTC+8, no DST
28: 	// anchor 1: local 2026-11-01 00:00 == 2026-10-31T16:00Z
29: 	if got := AnchorInstant(1, loc, 2026, time.November); !got.Equal(ts(t, "2026-10-31T16:00:00Z")) {
30: 		t.Fatalf("anchor 1 Nov 2026 = %s", got)
31: 	}
32: 	// anchor 15: local 2026-03-15 00:00 == 2026-03-14T16:00Z
33: 	if got := AnchorInstant(15, loc, 2026, time.March); !got.Equal(ts(t, "2026-03-14T16:00:00Z")) {
34: 		t.Fatalf("anchor 15 Mar 2026 = %s", got)
35: 	}
36: }
37: 
38: func TestAnchorInstantClipsShortMonths(t *testing.T) {
39: 	loc := mustLoc(t, "Asia/Shanghai")
40: 	// 2026 is not a leap year: anchor 31 -> 2026-02-28 local 00:00
41: 	if got := AnchorInstant(31, loc, 2026, time.February); !got.Equal(ts(t, "2026-02-27T16:00:00Z")) {
42: 		t.Fatalf("anchor 31 Feb 2026 = %s", got)
43: 	}
44: 	// 2028 is a leap year: anchor 31 -> 2028-02-29 local 00:00
45: 	if got := AnchorInstant(31, loc, 2028, time.February); !got.Equal(ts(t, "2028-02-28T16:00:00Z")) {
46: 		t.Fatalf("anchor 31 Feb 2028 = %s", got)
47: 	}
48: 	if d := ClampDay(31, 2026, time.April); d != 30 {
49: 		t.Fatalf("clamp April = %d", d)
50: 	}
51: }
52: 
53: func TestContainingAndNextBoundary(t *testing.T) {
54: 	loc := mustLoc(t, "Asia/Shanghai")
55: 	now := ts(t, "2026-10-20T06:00:00Z")
56: 	w := Containing(1, loc, now)
57: 	if !w.Start.Equal(ts(t, "2026-09-30T16:00:00Z")) || !w.End.Equal(ts(t, "2026-10-31T16:00:00Z")) {
58: 		t.Fatalf("containing anchor 1 = %+v", w)
59: 	}
60: 	// product.md §8.3 example: current 2026-03-15..2026-04-15 (anchor 15),
61: 	// then anchor 31 -> next cycle 2026-04-15..2026-04-30 local.
62: 	cur := Window(ts(t, "2026-03-14T16:00:00Z"), 15, loc)
63: 	if !cur.End.Equal(ts(t, "2026-04-14T16:00:00Z")) {
64: 		t.Fatalf("current end = %s", cur.End)
65: 	}
66: 	next := Window(cur.End, 31, loc)
67: 	if !next.Start.Equal(cur.End) || !next.End.Equal(ts(t, "2026-04-29T16:00:00Z")) {
68: 		t.Fatalf("next window under anchor 31 = %+v", next)
69: 	}
70: }
71: 
72: func TestNextBoundaryIsStrictlyIncreasing(t *testing.T) {
73: 	for _, tz := range []string{"Asia/Shanghai", "UTC", "America/Havana", "America/Santiago", "Australia/Lord_Howe"} {
74: 		loc := mustLoc(t, tz)
75: 		for _, anchor := range []int{1, 15, 28, 29, 30, 31} {
76: 			cur := AnchorInstant(anchor, loc, 2026, time.January)
77: 			for i := 0; i < 24; i++ {
78: 				nxt := NextBoundary(cur, anchor, loc)
79: 				if !nxt.After(cur) {
80: 					t.Fatalf("%s anchor %d not increasing: %s -> %s", tz, anchor, cur, nxt)
81: 				}
82: 				span := nxt.Sub(cur)
83: 				if span < 25*24*time.Hour || span > 32*24*time.Hour {
84: 					t.Fatalf("%s anchor %d implausible cycle length %v", tz, anchor, span)
85: 				}
86: 				cur = nxt
87: 			}
88: 		}
89: 	}
90: }
91: 
92: func TestDSTGapTakesFirstValidInstant(t *testing.T) {
93: 	loc := mustLoc(t, "America/Havana")
94: 	// Havana springs forward at 2026-03-08 00:00 -> 01:00, so local midnight
95: 	// does not exist. The first valid instant is 01:00 local (UTC-4) = 05:00Z.
96: 	got := AnchorInstant(8, loc, 2026, time.March)
97: 	if !got.Equal(ts(t, "2026-03-08T05:00:00Z")) {
98: 		t.Fatalf("gap resolution = %s", got)
99: 	}
100: 	local := got.In(loc)
101: 	if h, m, _ := local.Clock(); h != 1 || m != 0 {
102: 		t.Fatalf("gap resolution local time = %s", local)
103: 	}
104: }
105: 
106: func TestDSTAmbiguousTakesEarlierInstant(t *testing.T) {
107: 	loc := mustLoc(t, "America/Havana")
108: 	// Havana falls back at 2026-11-01 01:00 -> 00:00, so local midnight happens
109: 	// twice; the earlier absolute instant is 2026-11-01T04:00Z (UTC-4).
110: 	got := AnchorInstant(1, loc, 2026, time.November)
111: 	if !got.Equal(ts(t, "2026-11-01T04:00:00Z")) {
112: 		t.Fatalf("ambiguous resolution = %s", got)
113: 	}
114: }
115: 
116: func TestAllocateSampleAcrossBoundary(t *testing.T) {
117: 	b := Boundary{Start: ts(t, "2026-10-01T00:00:00Z"), End: ts(t, "2026-11-01T00:00:00Z")}
118: 	next := Boundary{Start: b.End, End: ts(t, "2026-12-01T00:00:00Z")}
119: 	// 60 minute sample starting 40 minutes before the boundary.
120: 	start := b.End.Add(-40 * time.Minute)
121: 	end := b.End.Add(20 * time.Minute)
122: 	got := AllocateSample(start, end, []Boundary{b, next}, 300, 600)
123: 	if len(got) != 2 {
124: 		t.Fatalf("expected 2 slices, got %+v", got)
125: 	}
126: 	if got[0].UpBytes != 200 || got[0].DownBytes != 400 {
127: 		t.Fatalf("first slice = %+v", got[0])
128: 	}
129: 	if got[1].UpBytes != 100 || got[1].DownBytes != 200 {
130: 		t.Fatalf("last slice takes the remainder = %+v", got[1])
131: 	}
132: 	if !got[0].Estimated || !got[0].Split || !got[1].Estimated {
133: 		t.Fatalf("split slices must be flagged estimated/boundary_split: %+v", got)
134: 	}
135: }
136: 
137: func TestAllocateSampleSinglePeriodIsExact(t *testing.T) {
138: 	b := Boundary{Start: ts(t, "2026-10-01T00:00:00Z"), End: ts(t, "2026-11-01T00:00:00Z")}
139: 	got := AllocateSample(ts(t, "2026-10-02T00:00:00Z"), ts(t, "2026-10-02T00:05:00Z"), []Boundary{b}, 11, 22)
140: 	if len(got) != 1 {
141: 		t.Fatalf("expected single slice, got %+v", got)
142: 	}
143: 	if got[0].UpBytes != 11 || got[0].DownBytes != 22 || got[0].Estimated || got[0].Split {
144: 		t.Fatalf("single period must be exact and not estimated: %+v", got[0])
145: 	}
146: }
147: 
148: func TestAllocateSampleRemainderGoesToLast(t *testing.T) {
149: 	p1 := Boundary{Start: ts(t, "2026-10-01T00:00:00Z"), End: ts(t, "2026-10-01T00:10:00Z")}
150: 	p2 := Boundary{Start: p1.End, End: ts(t, "2026-10-01T00:20:00Z")}
151: 	p3 := Boundary{Start: p2.End, End: ts(t, "2026-10-01T00:30:00Z")}
152: 	got := AllocateSample(p1.Start, p3.End, []Boundary{p1, p2, p3}, 100, 0)
153: 	if len(got) != 3 {
154: 		t.Fatalf("expected 3 slices, got %+v", got)
155: 	}
156: 	var sum int64
157: 	for _, a := range got {
158: 		sum += a.UpBytes
159: 	}
160: 	if sum != 100 {
161: 		t.Fatalf("slices must sum to the reported bytes, got %d", sum)
162: 	}
163: 	if got[0].UpBytes != 33 || got[1].UpBytes != 33 || got[2].UpBytes != 34 {
164: 		t.Fatalf("remainder to last segment: %+v", got)
165: 	}
166: }
167: 
168: func TestAllocateSampleZeroLengthIsDropped(t *testing.T) {
169: 	b := Boundary{Start: ts(t, "2026-10-01T00:00:00Z"), End: ts(t, "2026-11-01T00:00:00Z")}
170: 	if got := AllocateSample(ts(t, "2026-10-02T00:00:00Z"), ts(t, "2026-10-02T00:00:00Z"), []Boundary{b}, 1, 1); got != nil {
171: 		t.Fatalf("zero length sample must be dropped: %+v", got)
172: 	}
173: }
174: 
175: func TestLoadTimezoneRejectsEmptyAndUnknown(t *testing.T) {
176: 	if _, err := LoadTimezone(""); err == nil {
177: 		t.Fatal("empty timezone must fail")
178: 	}
179: 	if _, err := LoadTimezone("Mars/Olympus"); err == nil {
180: 		t.Fatal("unknown timezone must fail")
181: 	}
182: }
183: 
184: func TestContainingUsesLocalCalendarMonth(t *testing.T) {
185: 	for _, tc := range []struct {
186: 		name, zone, at, start, end string
187: 	}{
188: 		{"east-next-month", "Asia/Shanghai", "2026-10-31T17:00:00Z", "2026-10-31T16:00:00Z", "2026-11-30T16:00:00Z"},
189: 		{"east-next-year", "Pacific/Kiritimati", "2026-12-31T11:00:00Z", "2026-12-31T10:00:00Z", "2027-01-31T10:00:00Z"},
190: 		{"west-previous-month", "America/New_York", "2026-11-01T02:00:00Z", "2026-10-01T04:00:00Z", "2026-11-01T04:00:00Z"},
191: 	} {
192: 		t.Run(tc.name, func(t *testing.T) {
193: 			at := ts(t, tc.at)
194: 			got := Containing(1, mustLoc(t, tc.zone), at)
195: 			if !got.Start.Equal(ts(t, tc.start)) || !got.End.Equal(ts(t, tc.end)) {
196: 				t.Fatalf("containing cycle = %+v, want [%s,%s)", got, tc.start, tc.end)
197: 			}
198: 			if at.Before(got.Start) || !at.Before(got.End) {
199: 				t.Fatalf("cycle does not contain requested instant: %+v at %s", got, at)
200: 			}
201: 		})
202: 	}
203: }
204: 
205: func TestPrevBoundaryUsesLocalCalendarMonth(t *testing.T) {
206: 	for _, tc := range []struct {
207: 		name, zone, at, want string
208: 	}{
209: 		{"after-anchor", "Asia/Shanghai", "2026-10-31T17:00:00Z", "2026-10-31T16:00:00Z"},
210: 		{"exact-anchor-is-exclusive", "Asia/Shanghai", "2026-10-31T16:00:00Z", "2026-09-30T16:00:00Z"},
211: 		{"next-year", "Pacific/Kiritimati", "2026-12-31T11:00:00Z", "2026-12-31T10:00:00Z"},
212: 		{"west-previous-month", "America/New_York", "2026-11-01T02:00:00Z", "2026-10-01T04:00:00Z"},
213: 	} {
214: 		t.Run(tc.name, func(t *testing.T) {
215: 			if got := PrevBoundary(ts(t, tc.at), 1, mustLoc(t, tc.zone)); !got.Equal(ts(t, tc.want)) {
216: 				t.Fatalf("previous boundary = %s, want %s", got, tc.want)
217: 			}
218: 		})
219: 	}
220: }
221: 
222: func TestDSTGapAtLargeOffsetAndSkippedDate(t *testing.T) {
223: 	for _, tc := range []struct {
224: 		zone  string
225: 		year  int
226: 		month time.Month
227: 		day   int
228: 		want  string
229: 	}{
230: 		{"Asia/Tehran", 2021, time.March, 22, "2021-03-21T20:30:00Z"},
231: 		{"Pacific/Apia", 2011, time.December, 30, "2011-12-30T10:00:00Z"},
232: 	} {
233: 		t.Run(tc.zone, func(t *testing.T) {
234: 			if got := AnchorInstant(tc.day, mustLoc(t, tc.zone), tc.year, tc.month); !got.Equal(ts(t, tc.want)) {
235: 				t.Fatalf("first valid instant = %s, want %s", got, tc.want)
236: 			}
237: 		})
238: 	}
239: }
240: 
241: func TestLoadTimezoneRejectsHostDependentLocal(t *testing.T) {
242: 	if loc, err := LoadTimezone("Local"); err == nil {
243: 		t.Fatalf("host-dependent timezone was accepted: %v", loc)
244: 	}
245: }
246: 
247: func TestPrevBoundaryAndCandidateWindows(t *testing.T) {
248: 	loc := mustLoc(t, "Asia/Shanghai")
249: 	now := ts(t, "2026-10-20T06:00:00Z") // cycle 2026-09-30T16:00Z .. 2026-10-31T16:00Z
250: 	prev := PrevBoundary(ts(t, "2026-09-30T16:00:00Z"), 1, loc)
251: 	if !prev.Equal(ts(t, "2026-08-31T16:00:00Z")) {
252: 		t.Fatalf("prev boundary = %s", prev)
253: 	}
254: 	// sample starting two cycles back must be covered by three candidate windows
255: 	windows := CandidateWindows(1, loc, ts(t, "2026-08-20T00:00:00Z"), now)
256: 	if len(windows) != 3 {
257: 		t.Fatalf("windows = %+v", windows)
258: 	}
259: 	if !windows[0].Start.Equal(ts(t, "2026-09-30T16:00:00Z")) {
260: 		t.Fatalf("first window = %+v", windows[0])
261: 	}
262: 	if !windows[2].Start.Equal(ts(t, "2026-07-31T16:00:00Z")) {
263: 		t.Fatalf("oldest window = %+v", windows[2])
264: 	}
265: }
```
