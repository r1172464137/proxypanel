# CPREV-004 frozen input

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
diff --git a/internal/panel/store/access.go b/internal/panel/store/access.go
index 92a306a..fd95b12 100644
--- a/internal/panel/store/access.go
+++ b/internal/panel/store/access.go
@@ -518,7 +518,7 @@ func computeAdvance(m *domain.Member, sub *domain.Subscription, now time.Time) (
 	if err != nil {
 		return out, domain.Validation("invalid cycle timezone %q", tz)
 	}
-	win := period.Containing(anchor, loc, now)
+	win := period.Transition(*m.PeriodEnd, anchor, loc, now)
 	out.Start, out.End, out.Anchor, out.Timezone = win.Start, win.End, anchor, tz
 	out.Advanced = true
 	return out, nil

```

## internal/panel/store/access.go
```text
1: package store
2: 
3: import (
4: 	"context"
5: 	"database/sql"
6: 	"time"
7: 
8: 	"proxypanel/internal/panel/access"
9: 	"proxypanel/internal/panel/domain"
10: 	"proxypanel/internal/panel/period"
11: )
12: 
13: // Evidence is everything the single authorization algorithm needs for one
14: // member, loaded from PostgreSQL.
15: type Evidence struct {
16: 	Member       *domain.Member
17: 	Subscription *domain.Subscription
18: 	GroupIDs     []string
19: 	Nodes        []domain.Node
20: 	NodeGroups   map[string][]string
21: 	UsedBytes    int64
22: 	AdjustBytes  int64
23: }
24: 
25: // NodeGroupMap returns nodeID -> authorized group ids.
26: func (s *Store) NodeGroupMap(ctx context.Context) (map[string][]string, error) {
27: 	rows, err := s.DB.QueryContext(ctx, `SELECT node_id, group_id FROM node_group_members ORDER BY position`)
28: 	if err != nil {
29: 		return nil, dbErr(err)
30: 	}
31: 	defer rows.Close()
32: 	out := map[string][]string{}
33: 	for rows.Next() {
34: 		var nodeID, groupID string
35: 		if err := rows.Scan(&nodeID, &groupID); err != nil {
36: 			return nil, dbErr(err)
37: 		}
38: 		out[nodeID] = append(out[nodeID], groupID)
39: 	}
40: 	return out, dbErr(rows.Err())
41: }
42: 
43: // UsedBytes sums the ledger for a member's current cycle.
44: func (s *Store) UsedBytes(ctx context.Context, memberID string, periodStart *time.Time) (int64, error) {
45: 	var used int64
46: 	var err error
47: 	if periodStart == nil {
48: 		err = s.DB.QueryRowContext(ctx, `SELECT COALESCE(SUM(up_bytes+down_bytes),0) FROM usage_entries
49: 			WHERE member_id=$1 AND period_start IS NULL`, memberID).Scan(&used)
50: 	} else {
51: 		err = s.DB.QueryRowContext(ctx, `SELECT COALESCE(SUM(up_bytes+down_bytes),0) FROM usage_entries
52: 			WHERE member_id=$1 AND period_start=$2`, memberID, periodStart.UTC()).Scan(&used)
53: 	}
54: 	return used, dbErr(err)
55: }
56: 
57: // QuotaAdjustmentSum sums audited quota adjustments for the cycle. Adjustments
58: // never modify the original usage rows.
59: func (s *Store) QuotaAdjustmentSum(ctx context.Context, memberID string, periodStart *time.Time) (int64, error) {
60: 	return quotaAdjustmentSumQ(ctx, s.DB, memberID, periodStart)
61: }
62: 
63: // quotaAdjustmentSumQ is the transaction scoped variant of QuotaAdjustmentSum.
64: func quotaAdjustmentSumQ(ctx context.Context, q querier, memberID string, periodStart *time.Time) (int64, error) {
65: 	var sum int64
66: 	var err error
67: 	if periodStart == nil {
68: 		err = q.QueryRowContext(ctx, `SELECT COALESCE(SUM(bytes),0) FROM quota_adjustments
69: 			WHERE member_id=$1 AND period_start IS NULL`, memberID).Scan(&sum)
70: 	} else {
71: 		err = q.QueryRowContext(ctx, `SELECT COALESCE(SUM(bytes),0) FROM quota_adjustments
72: 			WHERE member_id=$1 AND period_start=$2`, memberID, periodStart.UTC()).Scan(&sum)
73: 	}
74: 	return sum, dbErr(err)
75: }
76: 
77: // Evidence loads the decision inputs for a member.
78: func (s *Store) Evidence(ctx context.Context, member *domain.Member) (*Evidence, error) {
79: 	ev := &Evidence{Member: member, NodeGroups: map[string][]string{}}
80: 	if member.SubscriptionID != nil {
81: 		sub, err := s.GetSubscription(ctx, *member.SubscriptionID)
82: 		if err != nil {
83: 			return nil, err
84: 		}
85: 		ev.Subscription = sub
86: 		ev.GroupIDs = sub.NodeGroupIDs
87: 	}
88: 	nodes, err := s.listAllNodes(ctx, "")
89: 	if err != nil {
90: 		return nil, err
91: 	}
92: 	ev.Nodes = nodes
93: 	groups, err := s.NodeGroupMap(ctx)
94: 	if err != nil {
95: 		return nil, err
96: 	}
97: 	ev.NodeGroups = groups
98: 	used, err := s.UsedBytes(ctx, member.ID, member.PeriodStart)
99: 	if err != nil {
100: 		return nil, err
101: 	}
102: 	ev.UsedBytes = used
103: 	adj, err := s.QuotaAdjustmentSum(ctx, member.ID, member.PeriodStart)
104: 	if err != nil {
105: 		return nil, err
106: 	}
107: 	ev.AdjustBytes = adj
108: 	return ev, nil
109: }
110: 
111: // ─── bulk decision inputs ──────────────────────────────────────────────────
112: 
113: // EvidenceBatch holds the decision inputs shared by many members. Loading them
114: // once keeps a snapshot build linear in the member count: the naive version
115: // reloaded every node, node group map and subscription once per member, which is
116: // N members x M nodes round trips and was the reason a 205 member snapshot never
117: // finished.
118: type EvidenceBatch struct {
119: 	Nodes         []domain.Node
120: 	NodeGroups    map[string][]string
121: 	subscriptions map[string]*domain.Subscription
122: 	used          map[string]int64
123: 	adjustments   map[string]int64
124: }
125: 
126: // cycleLedgerKey identifies one member's ledger slice. A nil period start means
127: // "no persisted cycle", which sums the whole ledger (same rule as UsedBytes).
128: func cycleLedgerKey(memberID string, periodStart *time.Time) string {
129: 	if periodStart == nil {
130: 		return memberID + "|\x00"
131: 	}
132: 	return memberID + "|" + periodStart.UTC().Format(time.RFC3339Nano)
133: }
134: 
135: // Evidence builds the single member evidence bundle from the batch, so the
136: // authorization algorithm still runs exactly once per member and on exactly the
137: // same inputs as Store.Evidence.
138: func (b *EvidenceBatch) Evidence(m *domain.Member) *Evidence {
139: 	ev := &Evidence{Member: m, Nodes: b.Nodes, NodeGroups: b.NodeGroups}
140: 	if m.SubscriptionID != nil {
141: 		if sub := b.subscriptions[*m.SubscriptionID]; sub != nil {
142: 			ev.Subscription = sub
143: 			ev.GroupIDs = sub.NodeGroupIDs
144: 		}
145: 	}
146: 	if b.used != nil {
147: 		ev.UsedBytes = b.used[cycleLedgerKey(m.ID, m.PeriodStart)]
148: 	}
149: 	if b.adjustments != nil {
150: 		ev.AdjustBytes = b.adjustments[cycleLedgerKey(m.ID, m.PeriodStart)]
151: 	}
152: 	return ev
153: }
154: 
155: // LoadEvidenceBatch loads the shared authorization inputs for the given members
156: // with a constant number of queries (nodes, node group map, subscriptions, one
157: // ledger aggregate and one adjustment aggregate).
158: func (s *Store) LoadEvidenceBatch(ctx context.Context, members []domain.Member) (*EvidenceBatch, error) {
159: 	b := &EvidenceBatch{
160: 		NodeGroups:    map[string][]string{},
161: 		subscriptions: map[string]*domain.Subscription{},
162: 		used:          map[string]int64{},
163: 		adjustments:   map[string]int64{},
164: 	}
165: 	nodes, err := s.listAllNodes(ctx, "")
166: 	if err != nil {
167: 		return nil, err
168: 	}
169: 	b.Nodes = nodes
170: 	groups, err := s.NodeGroupMap(ctx)
171: 	if err != nil {
172: 		return nil, err
173: 	}
174: 	b.NodeGroups = groups
175: 
176: 	memberIDs := make([]string, 0, len(members))
177: 	subIDs := make([]string, 0, len(members))
178: 	seenMember := map[string]bool{}
179: 	seenSub := map[string]bool{}
180: 	for i := range members {
181: 		if id := members[i].ID; id != "" && !seenMember[id] {
182: 			seenMember[id] = true
183: 			memberIDs = append(memberIDs, id)
184: 		}
185: 		if sub := members[i].SubscriptionID; sub != nil && !seenSub[*sub] {
186: 			seenSub[*sub] = true
187: 			subIDs = append(subIDs, *sub)
188: 		}
189: 	}
190: 	if len(subIDs) > 0 {
191: 		subs, err := s.loadSubscriptions(ctx, s.DB, subIDs)
192: 		if err != nil {
193: 			return nil, err
194: 		}
195: 		b.subscriptions = subs
196: 	}
197: 	if len(memberIDs) == 0 {
198: 		return b, nil
199: 	}
200: 	if err := s.sumLedgerByCycle(ctx, memberIDs, b.used); err != nil {
201: 		return nil, err
202: 	}
203: 	if err := s.sumAdjustmentsByCycle(ctx, memberIDs, b.adjustments); err != nil {
204: 		return nil, err
205: 	}
206: 	return b, nil
207: }
208: 
209: // loadSubscriptions loads the given subscriptions including their node groups.
210: func (s *Store) loadSubscriptions(ctx context.Context, q querier, ids []string) (map[string]*domain.Subscription, error) {
211: 	out := make(map[string]*domain.Subscription, len(ids))
212: 	if len(ids) == 0 {
213: 		return out, nil
214: 	}
215: 	rows, err := q.QueryContext(ctx, `SELECT `+subscriptionCols+` FROM subscriptions WHERE id = ANY($1)`, ids)
216: 	if err != nil {
217: 		return nil, dbErr(err)
218: 	}
219: 	for rows.Next() {
220: 		sub, err := scanSubscription(rows)
221: 		if err != nil {
222: 			rows.Close()
223: 			return nil, err
224: 		}
225: 		out[sub.ID] = sub
226: 	}
227: 	err = rows.Err()
228: 	rows.Close()
229: 	if err != nil {
230: 		return nil, dbErr(err)
231: 	}
232: 	groupRows, err := q.QueryContext(ctx, `SELECT subscription_id, node_group_id FROM subscription_node_groups
233: 		WHERE subscription_id = ANY($1) ORDER BY subscription_id, position`, ids)
234: 	if err != nil {
235: 		return nil, dbErr(err)
236: 	}
237: 	defer groupRows.Close()
238: 	for groupRows.Next() {
239: 		var subID, groupID string
240: 		if err := groupRows.Scan(&subID, &groupID); err != nil {
241: 			return nil, dbErr(err)
242: 		}
243: 		if sub := out[subID]; sub != nil {
244: 			sub.NodeGroupIDs = append(sub.NodeGroupIDs, groupID)
245: 		}
246: 	}
247: 	return out, dbErr(groupRows.Err())
248: }
249: 
250: // sumLedgerByCycle fills member x cycle usage totals in one aggregate query.
251: func (s *Store) sumLedgerByCycle(ctx context.Context, memberIDs []string, into map[string]int64) error {
252: 	rows, err := s.DB.QueryContext(ctx, `SELECT member_id, period_start,
253: 		COALESCE(SUM(up_bytes + down_bytes), 0)
254: 		FROM usage_entries WHERE member_id = ANY($1)
255: 		GROUP BY member_id, period_start`, memberIDs)
256: 	if err != nil {
257: 		return dbErr(err)
258: 	}
259: 	defer rows.Close()
260: 	for rows.Next() {
261: 		var memberID string
262: 		var periodStart sql.NullTime
263: 		var sum int64
264: 		if err := rows.Scan(&memberID, &periodStart, &sum); err != nil {
265: 			return dbErr(err)
266: 		}
267: 		var key time.Time
268: 		if periodStart.Valid {
269: 			key = periodStart.Time
270: 			into[cycleLedgerKey(memberID, &key)] = sum
271: 			continue
272: 		}
273: 		into[cycleLedgerKey(memberID, nil)] = sum
274: 	}
275: 	return dbErr(rows.Err())
276: }
277: 
278: // sumAdjustmentsByCycle fills member x cycle quota adjustment totals in one
279: // aggregate query.
280: func (s *Store) sumAdjustmentsByCycle(ctx context.Context, memberIDs []string, into map[string]int64) error {
281: 	rows, err := s.DB.QueryContext(ctx, `SELECT member_id, period_start, COALESCE(SUM(bytes), 0)
282: 		FROM quota_adjustments WHERE member_id = ANY($1)
283: 		GROUP BY member_id, period_start`, memberIDs)
284: 	if err != nil {
285: 		return dbErr(err)
286: 	}
287: 	defer rows.Close()
288: 	for rows.Next() {
289: 		var memberID string
290: 		var periodStart sql.NullTime
291: 		var sum int64
292: 		if err := rows.Scan(&memberID, &periodStart, &sum); err != nil {
293: 			return dbErr(err)
294: 		}
295: 		var key time.Time
296: 		if periodStart.Valid {
297: 			key = periodStart.Time
298: 			into[cycleLedgerKey(memberID, &key)] = sum
299: 			continue
300: 		}
301: 		into[cycleLedgerKey(memberID, nil)] = sum
302: 	}
303: 	return dbErr(rows.Err())
304: }
305: 
306: // activeCredentialSecrets returns memberID -> nodeID -> secret for the active
307: // credentials on the given nodes, decrypted in one pass.
308: func (s *Store) activeCredentialSecrets(ctx context.Context, nodeIDs []string) (map[string]map[string]string, error) {
309: 	out := map[string]map[string]string{}
310: 	if len(nodeIDs) == 0 {
311: 		return out, nil
312: 	}
313: 	rows, err := s.DB.QueryContext(ctx, `SELECT member_id, node_id, secret_enc FROM credentials
314: 		WHERE state='active' AND node_id = ANY($1)`, nodeIDs)
315: 	if err != nil {
316: 		return nil, dbErr(err)
317: 	}
318: 	defer rows.Close()
319: 	for rows.Next() {
320: 		var memberID, nodeID string
321: 		var enc []byte
322: 		if err := rows.Scan(&memberID, &nodeID, &enc); err != nil {
323: 			return nil, dbErr(err)
324: 		}
325: 		plain, err := s.decryptSecret(enc)
326: 		if err != nil || plain == "" {
327: 			continue
328: 		}
329: 		if out[memberID] == nil {
330: 			out[memberID] = map[string]string{}
331: 		}
332: 		out[memberID][nodeID] = plain
333: 	}
334: 	return out, dbErr(rows.Err())
335: }
336: 
337: // Decision evaluates the frozen algorithm for one evidence bundle.
338: func (ev *Evidence) Decision(now time.Time) access.Decision {
339: 	m := ev.Member
340: 	in := access.Input{
341: 		Now:                  now,
342: 		MemberID:             m.ID,
343: 		AccountActive:        m.Active,
344: 		AccountArchived:      m.Archived,
345: 		ProxyEnabled:         m.ProxyEnabled,
346: 		BindingPresent:       m.SubscriptionID != nil,
347: 		BindingExpiresAt:     m.ExpiresAt,
348: 		UsedBytes:            ev.UsedBytes,
349: 		OverrideQuotaEnabled: m.QuotaOverrideEnabled,
350: 		OverrideQuotaBytes:   m.QuotaOverrideBytes,
351: 	}
352: 	if m.SubscriptionID != nil {
353: 		in.SubscriptionID = *m.SubscriptionID
354: 	}
355: 	if ev.Subscription != nil {
356: 		in.SubscriptionAccessEnabled = ev.Subscription.AccessEnabled
357: 		in.SubscriptionArchived = ev.Subscription.Archived
358: 		in.SubscriptionQuotaBytes = ev.Subscription.QuotaBytes
359: 		in.AuthorizedGroupIDs = ev.Subscription.NodeGroupIDs
360: 	}
361: 	// Audited quota adjustments shift the effective quota for this cycle.
362: 	if ev.AdjustBytes != 0 {
363: 		base, _ := access.EffectiveQuota(in.SubscriptionQuotaBytes, in.OverrideQuotaBytes, in.OverrideQuotaEnabled)
364: 		if base != nil {
365: 			shifted := *base + ev.AdjustBytes
366: 			if shifted < 0 {
367: 				shifted = 0
368: 			}
369: 			in.OverrideQuotaEnabled = true
370: 			in.OverrideQuotaBytes = &shifted
371: 		}
372: 	}
373: 	states := make([]access.NodeState, 0, len(ev.Nodes))
374: 	for _, n := range ev.Nodes {
375: 		states = append(states, access.NodeState{ID: n.ID, Name: n.Name, Enabled: n.Enabled, GroupIDs: ev.NodeGroups[n.ID]})
376: 	}
377: 	in.Nodes = states
378: 	return access.Decide(in)
379: }
380: 
381: // AccessResult bundles a decision with the nodes it was computed from.
382: type AccessResult struct {
383: 	Decision access.Decision
384: 	Evidence *Evidence
385: }
386: 
387: // EvaluateAccess computes the decision for a member id.
388: func (s *Store) EvaluateAccess(ctx context.Context, memberID string) (*AccessResult, error) {
389: 	m, err := s.GetMember(ctx, memberID)
390: 	if err != nil {
391: 		return nil, err
392: 	}
393: 	return s.EvaluateAccessForMember(ctx, m)
394: }
395: 
396: // EvaluateAccessForMember computes the decision for a loaded member.
397: func (s *Store) EvaluateAccessForMember(ctx context.Context, m *domain.Member) (*AccessResult, error) {
398: 	// A cycle that ended must be rolled forward before the decision is taken:
399: 	// otherwise the authorization would be evaluated against the previous
400: 	// cycle's ledger and the previous cycle's rule.
401: 	if err := s.ensureMemberPeriod(ctx, m); err != nil {
402: 		return nil, err
403: 	}
404: 	ev, err := s.Evidence(ctx, m)
405: 	if err != nil {
406: 		return nil, err
407: 	}
408: 	d := ev.Decision(s.now())
409: 	m.UsedBytes = ev.UsedBytes
410: 	m.EffectiveQuotaBytes = d.EffectiveQuotaBytes
411: 	m.AccessReasons = d.Reasons
412: 	m.ProxyAccess = d.ProxyAccess
413: 	m.AllowedNodeIDs = d.AllowedNodeIDs
414: 	return &AccessResult{Decision: d, Evidence: ev}, nil
415: }
416: 
417: // AllowedNodes returns the node rows a member may currently use.
418: func (s *Store) AllowedNodes(ctx context.Context, res *AccessResult) []domain.Node {
419: 	allowed := map[string]bool{}
420: 	for _, id := range res.Decision.AllowedNodeIDs {
421: 		allowed[id] = true
422: 	}
423: 	out := make([]domain.Node, 0, len(allowed))
424: 	for _, n := range res.Evidence.Nodes {
425: 		if allowed[n.ID] {
426: 			out = append(out, n)
427: 		}
428: 	}
429: 	return out
430: }
431: 
432: // refreshMemberPeriod recomputes the persisted cycle of a member after a rule
433: // or binding change. The running cycle is preserved; only the next boundary
434: // follows the (possibly new) rule.
435: func (s *Store) refreshMemberPeriod(ctx context.Context, tx *sql.Tx, m *domain.Member) error {
436: 	if m.SubscriptionID == nil {
437: 		if _, err := tx.ExecContext(ctx, `UPDATE members SET period_start=NULL, period_end=NULL,
438: 			period_timezone='', period_anchor_day=0 WHERE id=$1`, m.ID); err != nil {
439: 			return dbErr(err)
440: 		}
441: 		m.PeriodStart, m.PeriodEnd = nil, nil
442: 		return nil
443: 	}
444: 	sub, err := scanSubscription(tx.QueryRowContext(ctx, `SELECT `+subscriptionCols+` FROM subscriptions WHERE id=$1`, *m.SubscriptionID))
445: 	if err != nil {
446: 		return err
447: 	}
448: 	anchor, tz := sub.AnchorDay, sub.Timezone
449: 	if m.PendingPeriodRule != nil {
450: 		anchor, tz = m.PendingPeriodRule.AnchorDay, m.PendingPeriodRule.Timezone
451: 	}
452: 	loc, err := period.LoadTimezone(tz)
453: 	if err != nil {
454: 		return domain.Validation("invalid timezone %q on subscription", tz)
455: 	}
456: 	now := s.now()
457: 	if m.PeriodStart == nil || m.PeriodEnd == nil || !m.PeriodEnd.After(now) {
458: 		win := period.Containing(anchor, loc, now)
459: 		if m.PeriodStart != nil && m.PeriodEnd != nil && !m.PeriodEnd.After(now) {
460: 			// roll forward to the cycle containing now
461: 			win = period.Containing(anchor, loc, now)
462: 		}
463: 		m.PeriodStart, m.PeriodEnd = &win.Start, &win.End
464: 	}
465: 	m.PeriodTimezone = tz
466: 	m.PeriodAnchorDay = anchor
467: 	if _, err := tx.ExecContext(ctx, `UPDATE members SET period_start=$2, period_end=$3, period_timezone=$4,
468: 		period_anchor_day=$5 WHERE id=$1`, m.ID, nullableTime(m.PeriodStart), nullableTime(m.PeriodEnd),
469: 		m.PeriodTimezone, m.PeriodAnchorDay); err != nil {
470: 		return dbErr(err)
471: 	}
472: 	return nil
473: }
474: 
475: // ─── authoritative cycle advancement ───────────────────────────────────────
476: 
477: // periodAdvance is the pure outcome of the cycle roll forward. It is pure so
478: // the boundary semantics stay testable without a database: a running cycle is
479: // never recomputed, and a rule submitted while the cycle was running takes
480: // effect exactly at the next boundary.
481: type periodAdvance struct {
482: 	Start, End  time.Time
483: 	Anchor      int
484: 	Timezone    string
485: 	Pending     *domain.PeriodRule
486: 	Advanced    bool
487: 	RuleApplied bool
488: }
489: 
490: // computeAdvance decides what the member's persisted cycle must be at now. It
491: // only ever moves an existing cycle forward: a member without a persisted cycle
492: // is left alone, so a read can never hand out a fresh (reset) cycle.
493: func computeAdvance(m *domain.Member, sub *domain.Subscription, now time.Time) (periodAdvance, error) {
494: 	out := periodAdvance{Anchor: m.PeriodAnchorDay, Timezone: m.PeriodTimezone, Pending: m.PendingPeriodRule}
495: 	if m.PeriodStart == nil || m.PeriodEnd == nil {
496: 		return out, nil
497: 	}
498: 	anchor, tz := m.PeriodAnchorDay, m.PeriodTimezone
499: 	if anchor == 0 || tz == "" {
500: 		if sub == nil {
501: 			return out, nil
502: 		}
503: 		anchor, tz = sub.AnchorDay, sub.Timezone
504: 	}
505: 	if m.PeriodEnd.After(now) {
506: 		// The running cycle stays authoritative, whatever the subscription now
507: 		// says; a pending rule waits for the next boundary.
508: 		out.Start, out.End, out.Anchor, out.Timezone = *m.PeriodStart, *m.PeriodEnd, anchor, tz
509: 		return out, nil
510: 	}
511: 	if p := out.Pending; p != nil {
512: 		// The cycle ended, so the submitted rule becomes the running rule.
513: 		anchor, tz = p.AnchorDay, p.Timezone
514: 		out.Pending = nil
515: 		out.RuleApplied = true
516: 	}
517: 	loc, err := period.LoadTimezone(tz)
518: 	if err != nil {
519: 		return out, domain.Validation("invalid cycle timezone %q", tz)
520: 	}
521: 	win := period.Transition(*m.PeriodEnd, anchor, loc, now)
522: 	out.Start, out.End, out.Anchor, out.Timezone = win.Start, win.End, anchor, tz
523: 	out.Advanced = true
524: 	return out, nil
525: }
526: 
527: // advanceMemberPeriodTx rolls the member's cycle forward inside the caller's
528: // transaction, then recomputes the persisted quota verdict for the new cycle
529: // and publishes the result with a revision bump in that same transaction. The
530: // caller must already hold the member row (or accept the extra row lock).
531: func (s *Store) advanceMemberPeriodTx(ctx context.Context, tx *sql.Tx, m *domain.Member, now time.Time) (bool, error) {
532: 	var sub *domain.Subscription
533: 	if m.SubscriptionID != nil {
534: 		loaded, err := scanSubscription(tx.QueryRowContext(ctx, `SELECT `+subscriptionCols+` FROM subscriptions WHERE id=$1`,
535: 			*m.SubscriptionID))
536: 		if err != nil {
537: 			return false, err
538: 		}
539: 		sub = loaded
540: 	}
541: 	adv, err := computeAdvance(m, sub, now)
542: 	if err != nil {
543: 		return false, err
544: 	}
545: 	if !adv.Advanced {
546: 		return false, nil
547: 	}
548: 	start, end := adv.Start, adv.End
549: 	m.PeriodStart, m.PeriodEnd = &start, &end
550: 	m.PeriodAnchorDay, m.PeriodTimezone = adv.Anchor, adv.Timezone
551: 	m.PendingPeriodRule = adv.Pending
552: 	if _, err := tx.ExecContext(ctx, `UPDATE members SET period_start=$2, period_end=$3, period_timezone=$4,
553: 		period_anchor_day=$5, pending_period_rule=$6, updated_at=now() WHERE id=$1`,
554: 		m.ID, nullableTime(m.PeriodStart), nullableTime(m.PeriodEnd), m.PeriodTimezone, m.PeriodAnchorDay,
555: 		pendingJSON(m.PendingPeriodRule)); err != nil {
556: 		return false, dbErr(err)
557: 	}
558: 	// The new cycle starts from an empty ledger slice: a member that was over
559: 	// quota gets its access back, and the agents must be told so now instead of
560: 	// on the next unrelated write.
561: 	if err := s.applyQuotaState(ctx, tx, m.ID); err != nil {
562: 		return false, err
563: 	}
564: 	if _, err := s.bumpRevisionsTx(ctx, tx, nil); err != nil {
565: 		return false, err
566: 	}
567: 	return true, nil
568: }
569: 
570: // ensureMemberPeriod advances a member outside a transaction and refreshes the
571: // in memory row. A running cycle costs one comparison, so per request read
572: // paths can call it for every member they evaluate.
573: func (s *Store) ensureMemberPeriod(ctx context.Context, m *domain.Member) error {
574: 	if m == nil || m.SubscriptionID == nil || m.PeriodStart == nil || m.PeriodEnd == nil {
575: 		return nil
576: 	}
577: 	now := s.now()
578: 	if m.PeriodEnd.After(now) {
579: 		return nil
580: 	}
581: 	return s.inTx(ctx, func(tx *sql.Tx) error {
582: 		cur, err := scanMember(tx.QueryRowContext(ctx, `SELECT `+memberCols+` FROM members WHERE id=$1 FOR UPDATE`, m.ID))
583: 		if err != nil {
584: 			return err
585: 		}
586: 		advanced, err := s.advanceMemberPeriodTx(ctx, tx, cur, now)
587: 		if err != nil {
588: 			return err
589: 		}
590: 		if !advanced {
591: 			return nil
592: 		}
593: 		m.PeriodStart, m.PeriodEnd = cur.PeriodStart, cur.PeriodEnd
594: 		m.PeriodAnchorDay, m.PeriodTimezone = cur.PeriodAnchorDay, cur.PeriodTimezone
595: 		m.PendingPeriodRule, m.OverQuota = cur.PendingPeriodRule, cur.OverQuota
596: 		return nil
597: 	})
598: }
599: 
600: // AdvancePeriods is the authoritative cycle tick. It rolls every member whose
601: // cycle ended into the cycle containing now, applies pending rules at the
602: // boundary, recomputes over_quota and publishes the revision, each in its own
603: // transaction. It is safe to call from a background ticker and from any code
604: // path that cannot advance inline; the authorization read paths call
605: // ensureMemberPeriod for the members they touch.
606: func (s *Store) AdvancePeriods(ctx context.Context, now time.Time) (int, error) {
607: 	if now.IsZero() {
608: 		now = s.now()
609: 	}
610: 	now = now.UTC()
611: 	// Only members that already have a persisted cycle are advanced: a member
612: 	// without one keeps the legacy evaluation and is never handed a fresh
613: 	// (reset) cycle by a background pass.
614: 	rows, err := s.DB.QueryContext(ctx, `SELECT id FROM members
615: 		WHERE subscription_id IS NOT NULL AND period_start IS NOT NULL AND period_end IS NOT NULL
616: 		  AND period_end <= $1
617: 		ORDER BY id LIMIT 5000`, now)
618: 	if err != nil {
619: 		return 0, dbErr(err)
620: 	}
621: 	ids := make([]string, 0, 16)
622: 	for rows.Next() {
623: 		var id string
624: 		if err := rows.Scan(&id); err != nil {
625: 			rows.Close()
626: 			return 0, dbErr(err)
627: 		}
628: 		ids = append(ids, id)
629: 	}
630: 	err = rows.Err()
631: 	rows.Close()
632: 	if err != nil {
633: 		return 0, dbErr(err)
634: 	}
635: 	advanced := 0
636: 	for _, id := range ids {
637: 		err := s.inTx(ctx, func(tx *sql.Tx) error {
638: 			cur, err := scanMember(tx.QueryRowContext(ctx, `SELECT `+memberCols+` FROM members WHERE id=$1 FOR UPDATE`, id))
639: 			if err != nil {
640: 				return err
641: 			}
642: 			ok, err := s.advanceMemberPeriodTx(ctx, tx, cur, now)
643: 			if ok {
644: 				advanced++
645: 			}
646: 			return err
647: 		})
648: 		if err != nil {
649: 			return advanced, err
650: 		}
651: 	}
652: 	return advanced, nil
653: }
```

## internal/panel/store/usage.go
```text
1: package store
2: 
3: import (
4: 	"context"
5: 	"database/sql"
6: 	"time"
7: 
8: 	"proxypanel/internal/contracts"
9: 	"proxypanel/internal/panel/domain"
10: 	"proxypanel/internal/panel/ids"
11: 	"proxypanel/internal/panel/period"
12: 	"proxypanel/internal/panel/usagecalc"
13: )
14: 
15: // SubmitUsageBatch accounts an agent batch exactly once. The ack is only
16: // produced after the PostgreSQL transaction commits.
17: func (s *Store) SubmitUsageBatch(ctx context.Context, agent *domain.Agent, batch contracts.UsageBatch) (status string, err error) {
18: 	hash := usagecalc.PayloadSHA256(batch)
19: 	status = "accepted"
20: 	err = s.inTx(ctx, func(tx *sql.Tx) error {
21: 		var existing string
22: 		err := tx.QueryRowContext(ctx, `SELECT payload_sha256 FROM usage_batches
23: 			WHERE agent_id=$1 AND batch_id=$2 FOR UPDATE`, agent.ID, batch.BatchID).Scan(&existing)
24: 		switch {
25: 		case err == nil:
26: 			if existing != hash {
27: 				return domain.E(domain.CodeBatchPayloadMismatch, 409,
28: 					"batch_id already exists with a different payload").
29: 					WithDetail("batch_id", batch.BatchID).WithDetail("existing_payload_sha256", existing)
30: 			}
31: 			status = "duplicate"
32: 			return nil
33: 		case err != sql.ErrNoRows:
34: 			return dbErr(err)
35: 		}
36: 
37: 		validator := usagecalc.Validator{
38: 			Now:           s.now(),
39: 			AgentServerID: agent.ServerID,
40: 			NodeServer: func(nodeID string) (string, bool) {
41: 				var serverID string
42: 				if err := tx.QueryRowContext(ctx, `SELECT server_id FROM nodes WHERE id=$1`, nodeID).Scan(&serverID); err != nil {
43: 					return "", false
44: 				}
45: 				return serverID, true
46: 			},
47: 			NodeHasHistory: func(nodeID, memberID string) bool {
48: 				var n int
49: 				if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM credentials WHERE node_id=$1 AND member_id=$2`,
50: 					nodeID, memberID).Scan(&n); err != nil {
51: 					return false
52: 				}
53: 				return n > 0
54: 			},
55: 		}
56: 		if rejects := usagecalc.Validate(batch, validator); len(rejects) != 0 {
57: 			code := usagecalc.RejectCodePriority(rejects)
58: 			status := 400
59: 			switch code {
60: 			case "BATCH_OWNERSHIP_MISMATCH":
61: 				status = 403
62: 			case "BATCH_CLOCK_SKEW":
63: 				status = 422
64: 			}
65: 			e := domain.E(code, status, "usage batch rejected: %s", rejects[0].Message)
66: 			e.Details = map[string]any{"rejects": rejects}
67: 			return e
68: 		}
69: 
70: 		startedAt, endedAt := batchBounds(batch.Samples)
71: 		if _, err := tx.ExecContext(ctx, `INSERT INTO usage_batches
72: 			(agent_id, batch_id, payload_sha256, status, sample_count, started_at, ended_at, core_generation)
73: 			VALUES ($1,$2,$3,'accepted',$4,$5,$6,$7)`,
74: 			agent.ID, batch.BatchID, hash, len(batch.Samples), nullableTime(startedAt), nullableTime(endedAt),
75: 			firstGeneration(batch.Samples)); err != nil {
76: 			return dbErr(err)
77: 		}
78: 
79: 		touched := map[string]bool{}
80: 		for _, sample := range batch.Samples {
81: 			// The member row is locked so the cycle advance below cannot race
82: 			// with another batch or a read path for the same member.
83: 			m, err := scanMember(tx.QueryRowContext(ctx, `SELECT `+memberCols+` FROM members WHERE id=$1 FOR UPDATE`, sample.MemberID))
84: 			if err != nil {
85: 				if de, ok := err.(*domain.Error); ok && de.Code == domain.CodeNotFound {
86: 					return domain.E(domain.CodeBatchOwnershipMismatch, 403, "unknown member in usage batch")
87: 				}
88: 				return err
89: 			}
90: 			// Accounting for the cycle that contains now is only correct once the
91: 			// cycle itself was rolled forward: otherwise a new month's bytes are
92: 			// booked against the previous period_start, which is what made the
93: 			// quota never reset.
94: 			if _, err := s.advanceMemberPeriodTx(ctx, tx, m, s.now()); err != nil {
95: 				return err
96: 			}
97: 			anchor, tz := m.PeriodAnchorDay, m.PeriodTimezone
98: 			if anchor == 0 || tz == "" {
99: 				// Member without a cycle rule: fall back to the global settings
100: 				// timezone and anchor day 1 rather than dropping data.
101: 				st, serr := settingsQ(ctx, tx)
102: 				if serr != nil {
103: 					return serr
104: 				}
105: 				anchor, tz = 1, st.Timezone
106: 			}
107: 			loc, err := period.LoadTimezone(tz)
108: 			if err != nil {
109: 				return err
110: 			}
111: 			windows := period.CandidateWindows(anchor, loc, sample.StartedAt, s.now())
112: 			var currentStart time.Time
113: 			if m.PeriodStart != nil {
114: 				currentStart = *m.PeriodStart
115: 			}
116: 			allocs := usagecalc.Allocate(sample, windows, currentStart)
117: 			if len(allocs) == 0 {
118: 				continue
119: 			}
120: 			credID := ""
121: 			_ = tx.QueryRowContext(ctx, `SELECT id FROM credentials WHERE node_id=$1 AND member_id=$2
122: 				ORDER BY state='active' DESC, created_at DESC LIMIT 1`, sample.NodeID, sample.MemberID).Scan(&credID)
123: 			for i, a := range allocs {
124: 				if _, err := tx.ExecContext(ctx, `INSERT INTO usage_entries
125: 					(agent_id, batch_id, member_id, node_id, credential_id, period_start, period_end, up_bytes, down_bytes,
126: 					 sample_started_at, sample_ended_at, estimated, boundary_split, correction, core_generation, split_part)
127: 					VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)
128: 					ON CONFLICT DO NOTHING`,
129: 					agent.ID, batch.BatchID, sample.MemberID, sample.NodeID, credID, a.PeriodStart, a.PeriodEnd,
130: 					a.UpBytes, a.DownBytes, sample.StartedAt.UTC(), sample.EndedAt.UTC(), a.Estimated,
131: 					a.BoundarySplit, a.Correction, sample.CoreGeneration, i); err != nil {
132: 					return dbErr(err)
133: 				}
134: 			}
135: 			touched[sample.MemberID] = true
136: 		}
137: 
138: 		// Quota enforcement: the ledger is authoritative, excess revokes access
139: 		// in the same transaction and schedules a revision bump.
140: 		for memberID := range touched {
141: 			if err := s.applyQuotaState(ctx, tx, memberID); err != nil {
142: 				return err
143: 			}
144: 		}
145: 		return nil
146: 	})
147: 	if err != nil {
148: 		return "", err
149: 	}
150: 	return status, nil
151: }
152: 
153: // applyQuotaState re-evaluates the ledger for the member's current cycle and
154: // flips over_quota, publishing the change (revision bump) in the same
155: // transaction. Both directions are published: a quota increase or a new cycle
156: // restores access and the agents must learn it now.
157: func (s *Store) applyQuotaState(ctx context.Context, tx *sql.Tx, memberID string) error {
158: 	m, err := scanMember(tx.QueryRowContext(ctx, `SELECT `+memberCols+` FROM members WHERE id=$1 FOR UPDATE`, memberID))
159: 	if err != nil {
160: 		return err
161: 	}
162: 	used, err := s.usedBytesQ(ctx, tx, memberID, m.PeriodStart)
163: 	if err != nil {
164: 		return err
165: 	}
166: 	quota, err := effectiveQuotaQ(ctx, tx, m)
167: 	if err != nil {
168: 		return err
169: 	}
170: 	over := quota != nil && used >= *quota
171: 	if over == m.OverQuota {
172: 		return nil
173: 	}
174: 	if _, err := tx.ExecContext(ctx, `UPDATE members SET over_quota=$2, updated_at=now() WHERE id=$1`, memberID, over); err != nil {
175: 		return dbErr(err)
176: 	}
177: 	if _, err := s.bumpRevisionsTx(ctx, tx, nil); err != nil {
178: 		return err
179: 	}
180: 	if over {
181: 		if _, err := tx.ExecContext(ctx, `INSERT INTO events (id, kind, severity, subject_type, subject_id, message, dedupe_key)
182: 			VALUES ($1,'member_over_quota','warning','member',$2,'member reached the effective quota',$3)
183: 			ON CONFLICT DO NOTHING`, newID(ids.Event), memberID, "over_quota:"+memberID); err != nil {
184: 			return dbErr(err)
185: 		}
186: 	}
187: 	return nil
188: }
189: 
190: // effectiveQuotaQ returns the quota that applies to the member's running cycle,
191: // including audited quota adjustments, or nil when it is unlimited.
192: func effectiveQuotaQ(ctx context.Context, q querier, m *domain.Member) (*int64, error) {
193: 	var base *int64
194: 	if m.QuotaOverrideEnabled {
195: 		base = m.QuotaOverrideBytes
196: 	} else if m.SubscriptionID != nil {
197: 		var quota sql.NullInt64
198: 		err := q.QueryRowContext(ctx, `SELECT quota_bytes FROM subscriptions WHERE id=$1`, *m.SubscriptionID).Scan(&quota)
199: 		switch {
200: 		case err == sql.ErrNoRows:
201: 		case err != nil:
202: 			return nil, dbErr(err)
203: 		case quota.Valid:
204: 			v := quota.Int64
205: 			base = &v
206: 		}
207: 	}
208: 	adj, err := quotaAdjustmentSumQ(ctx, q, m.ID, m.PeriodStart)
209: 	if err != nil {
210: 		return nil, err
211: 	}
212: 	if base == nil || adj == 0 {
213: 		return base, nil
214: 	}
215: 	shifted := *base + adj
216: 	if shifted < 0 {
217: 		shifted = 0
218: 	}
219: 	return &shifted, nil
220: }
221: func (s *Store) usedBytesQ(ctx context.Context, q querier, memberID string, periodStart *time.Time) (int64, error) {
222: 	var used int64
223: 	var err error
224: 	if periodStart == nil {
225: 		err = q.QueryRowContext(ctx, `SELECT COALESCE(SUM(up_bytes+down_bytes),0) FROM usage_entries WHERE member_id=$1`, memberID).Scan(&used)
226: 	} else {
227: 		err = q.QueryRowContext(ctx, `SELECT COALESCE(SUM(up_bytes+down_bytes),0) FROM usage_entries
228: 			WHERE member_id=$1 AND period_start=$2`, memberID, periodStart.UTC()).Scan(&used)
229: 	}
230: 	return used, dbErr(err)
231: }
232: 
233: func batchBounds(samples []contracts.UsageSample) (*time.Time, *time.Time) {
234: 	if len(samples) == 0 {
235: 		return nil, nil
236: 	}
237: 	start, end := samples[0].StartedAt, samples[0].EndedAt
238: 	for _, s := range samples[1:] {
239: 		if s.StartedAt.Before(start) {
240: 			start = s.StartedAt
241: 		}
242: 		if s.EndedAt.After(end) {
243: 			end = s.EndedAt
244: 		}
245: 	}
246: 	su, eu := start.UTC(), end.UTC()
247: 	return &su, &eu
248: }
249: 
250: func firstGeneration(samples []contracts.UsageSample) string {
251: 	if len(samples) == 0 {
252: 		return ""
253: 	}
254: 	return samples[0].CoreGeneration
255: }
256: 
257: // UsageFilter selects ledger rows.
258: type UsageFilter struct {
259: 	MemberID string
260: 	NodeID   string
261: 	From     *time.Time
262: 	To       *time.Time
263: 	Limit    int
264: }
265: 
266: // ListUsage aggregates ledger rows per member x node x cycle.
267: func (s *Store) ListUsage(ctx context.Context, f UsageFilter) ([]domain.UsageRow, error) {
268: 	q := `SELECT member_id, node_id, period_start, period_end, SUM(up_bytes), SUM(down_bytes),
269: 		BOOL_OR(estimated), BOOL_OR(boundary_split), BOOL_OR(correction)
270: 		FROM usage_entries WHERE true`
271: 	args := []any{}
272: 	if f.MemberID != "" {
273: 		args = append(args, f.MemberID)
274: 		q += ` AND member_id=$` + itoa(len(args))
275: 	}
276: 	if f.NodeID != "" {
277: 		args = append(args, f.NodeID)
278: 		q += ` AND node_id=$` + itoa(len(args))
279: 	}
280: 	if f.From != nil {
281: 		args = append(args, f.From.UTC())
282: 		q += ` AND period_start >= $` + itoa(len(args))
283: 	}
284: 	if f.To != nil {
285: 		args = append(args, f.To.UTC())
286: 		q += ` AND period_start < $` + itoa(len(args))
287: 	}
288: 	args = append(args, clampLimit(f.Limit))
289: 	q += ` GROUP BY member_id, node_id, period_start, period_end ORDER BY period_start DESC LIMIT $` + itoa(len(args))
290: 	rows, err := s.DB.QueryContext(ctx, q, args...)
291: 	if err != nil {
292: 		return nil, dbErr(err)
293: 	}
294: 	defer rows.Close()
295: 	out := make([]domain.UsageRow, 0, 8)
296: 	for rows.Next() {
297: 		var v domain.UsageRow
298: 		if err := rows.Scan(&v.MemberID, &v.NodeID, &v.PeriodStart, &v.PeriodEnd, &v.UpBytes, &v.DownBytes,
299: 			&v.Estimated, &v.BoundarySplit, &v.Correction); err != nil {
300: 			return nil, dbErr(err)
301: 		}
302: 		v.PeriodStart = utc(v.PeriodStart)
303: 		v.PeriodEnd = utc(v.PeriodEnd)
304: 		v.Direction = usagecalc.Direction
305: 		v.AlgorithmVersion = usagecalc.MeteringAlgorithmVersion
306: 		out = append(out, v)
307: 	}
308: 	return out, dbErr(rows.Err())
309: }
310: 
311: // CreateQuotaAdjustment records an audited quota change for the current cycle.
312: // The adjustment, the recomputed quota verdict and the revision that publishes
313: // it share one transaction, so a lowered quota cannot commit without revoking.
314: func (s *Store) CreateQuotaAdjustment(ctx context.Context, memberID string, bytes int64, reason, actor string) (*domain.QuotaAdjustment, error) {
315: 	adj := &domain.QuotaAdjustment{
316: 		ID: newID(ids.QuotaAdjustment), MemberID: memberID, Bytes: bytes, Reason: reason,
317: 		CreatedBy: actor, CreatedAt: s.now(),
318: 	}
319: 	err := s.inTx(ctx, func(tx *sql.Tx) error {
320: 		m, err := scanMember(tx.QueryRowContext(ctx, `SELECT `+memberCols+` FROM members WHERE id=$1 FOR UPDATE`, memberID))
321: 		if err != nil {
322: 			return err
323: 		}
324: 		adj.PeriodStart = m.PeriodStart
325: 		if _, err := tx.ExecContext(ctx, `INSERT INTO quota_adjustments (id, member_id, bytes, reason, period_start, created_by, created_at)
326: 			VALUES ($1,$2,$3,$4,$5,$6,$7)`, adj.ID, adj.MemberID, adj.Bytes, adj.Reason,
327: 			nullableTime(adj.PeriodStart), adj.CreatedBy, adj.CreatedAt); err != nil {
328: 			return dbErr(err)
329: 		}
330: 		// Recompute instead of blindly clearing the flag: a negative adjustment
331: 		// that puts the quota below the used bytes keeps the member revoked.
332: 		if err := s.applyQuotaState(ctx, tx, memberID); err != nil {
333: 			return err
334: 		}
335: 		_, err = s.bumpRevisionsTx(ctx, tx, nil)
336: 		return err
337: 	})
338: 	if err != nil {
339: 		return nil, err
340: 	}
341: 	return adj, nil
342: }
343: 
344: func (s *Store) ListQuotaAdjustments(ctx context.Context, memberID string, limit int) ([]domain.QuotaAdjustment, error) {
345: 	q := `SELECT id, member_id, bytes, reason, period_start, created_by, created_at FROM quota_adjustments`
346: 	args := []any{}
347: 	if memberID != "" {
348: 		args = append(args, memberID)
349: 		q += ` WHERE member_id=$1`
350: 	}
351: 	args = append(args, clampLimit(limit))
352: 	q += ` ORDER BY created_at DESC LIMIT $` + itoa(len(args))
353: 	rows, err := s.DB.QueryContext(ctx, q, args...)
354: 	if err != nil {
355: 		return nil, dbErr(err)
356: 	}
357: 	defer rows.Close()
358: 	out := make([]domain.QuotaAdjustment, 0, 4)
359: 	for rows.Next() {
360: 		var v domain.QuotaAdjustment
361: 		var ps sql.NullTime
362: 		if err := rows.Scan(&v.ID, &v.MemberID, &v.Bytes, &v.Reason, &ps, &v.CreatedBy, &v.CreatedAt); err != nil {
363: 			return nil, dbErr(err)
364: 		}
365: 		if ps.Valid {
366: 			t := ps.Time.UTC()
367: 			v.PeriodStart = &t
368: 		}
369: 		v.CreatedAt = utc(v.CreatedAt)
370: 		out = append(out, v)
371: 	}
372: 	return out, dbErr(rows.Err())
373: }
```

## internal/panel/usagecalc/usagecalc.go
```text
1: // Package usagecalc validates agent usage batches and turns samples into ledger
2: // allocations. Rules: docs/specs/product.md §8.5 and plan §3 F09.
3: package usagecalc
4: 
5: import (
6: 	"crypto/sha256"
7: 	"encoding/hex"
8: 	"encoding/json"
9: 	"fmt"
10: 	"math"
11: 	"time"
12: 
13: 	"proxypanel/internal/contracts"
14: 	"proxypanel/internal/panel/period"
15: )
16: 
17: // MeteringAlgorithmVersion is shared with the agent and recorded on ledger rows.
18: const MeteringAlgorithmVersion = "metre/v1"
19: 
20: // Direction documents the counter source: ingress per user, which avoids double
21: // counting at the exit.
22: const Direction = "ingress_user"
23: 
24: // Limits guard payload size and clock sanity. They are defaults overridable by
25: // settings; the values are recorded in the implementation report.
26: const (
27: 	DefaultSkewTolerance = 5 * time.Minute
28: 	DefaultMaxSamples    = 10000
29: 	DefaultMaxBatchBytes = 1 << 20 // serialized JSON bytes, NOT metered traffic
30: 	MaxSampleDuration    = 6 * time.Hour
31: )
32: 
33: // Reject describes one refused sample without exposing secrets.
34: type Reject struct {
35: 	Index   int    `json:"index"`
36: 	Code    string `json:"code"`
37: 	Field   string `json:"field"`
38: 	Message string `json:"message"`
39: }
40: 
41: // Validator supplies ownership and history lookups; the store implements it
42: // with PostgreSQL. Historical credentials count: a member who was disabled or
43: // rotated still has valid history and must be credited.
44: type Validator struct {
45: 	Now            time.Time
46: 	SkewTolerance  time.Duration
47: 	MaxSamples     int
48: 	MaxBatchBytes  int64
49: 	AgentServerID  string
50: 	NodeServer     func(nodeID string) (serverID string, exists bool)
51: 	NodeHasHistory func(nodeID, memberID string) bool
52: }
53: 
54: // PayloadSHA256 is the canonical fingerprint used for batch idempotency.
55: func PayloadSHA256(batch contracts.UsageBatch) string {
56: 	sum := sha256.Sum256(canonicalJSON(batch))
57: 	return hex.EncodeToString(sum[:])
58: }
59: 
60: func canonicalJSON(batch contracts.UsageBatch) []byte {
61: 	// Contracts structs have fixed field order, so marshalling is canonical.
62: 	// SchemaVersion is included: the same batch id with a different protocol
63: 	// version is a different payload and must be rejected.
64: 	raw, err := json.Marshal(struct {
65: 		SchemaVersion string                  `json:"schema_version"`
66: 		BatchID       string                  `json:"batch_id"`
67: 		Samples       []contracts.UsageSample `json:"samples"`
68: 	}{batch.SchemaVersion, batch.BatchID, batch.Samples})
69: 	if err != nil {
70: 		return []byte(batch.BatchID)
71: 	}
72: 	return raw
73: }
74: 
75: // Validate checks structural limits and per-sample ownership/values. The
76: // returned rejects are non-empty when the batch must not be accounted.
77: func Validate(batch contracts.UsageBatch, v Validator) []Reject {
78: 	var rejects []Reject
79: 	now := v.Now
80: 	if now.IsZero() {
81: 		now = time.Now().UTC()
82: 	}
83: 	skew := v.SkewTolerance
84: 	if skew <= 0 {
85: 		skew = DefaultSkewTolerance
86: 	}
87: 	maxSamples := v.MaxSamples
88: 	if maxSamples <= 0 {
89: 		maxSamples = DefaultMaxSamples
90: 	}
91: 	maxBytes := v.MaxBatchBytes
92: 	if maxBytes <= 0 {
93: 		maxBytes = DefaultMaxBatchBytes
94: 	}
95: 	if raw, err := json.Marshal(batch); err != nil || int64(len(raw)) > maxBytes {
96: 		rejects = append(rejects, Reject{Index: -1, Code: "VALIDATION_FAILED", Field: "payload", Message: "serialized usage payload exceeds the accepted limit"})
97: 	}
98: 	if batch.BatchID == "" {
99: 		rejects = append(rejects, Reject{Index: -1, Code: "VALIDATION_FAILED", Field: "batch_id", Message: "batch_id is required"})
100: 	}
101: 	if len(batch.Samples) > maxSamples {
102: 		rejects = append(rejects, Reject{Index: -1, Code: "VALIDATION_FAILED", Field: "samples", Message: fmt.Sprintf("batch has %d samples, limit is %d", len(batch.Samples), maxSamples)})
103: 	}
104: 	if batch.SchemaVersion != "" && batch.SchemaVersion != contracts.SchemaVersion {
105: 		rejects = append(rejects, Reject{Index: -1, Code: "SNAPSHOT_SCHEMA_UNSUPPORTED", Field: "schema_version", Message: "unsupported schema version " + batch.SchemaVersion})
106: 	}
107: 	var total int64
108: 	for i, s := range batch.Samples {
109: 		add := func(code, field, msg string) {
110: 			rejects = append(rejects, Reject{Index: i, Code: code, Field: field, Message: msg})
111: 		}
112: 		switch {
113: 		case s.NodeID == "":
114: 			add("VALIDATION_FAILED", "node_id", "node_id is required")
115: 			continue
116: 		case s.MemberID == "":
117: 			add("VALIDATION_FAILED", "member_id", "member_id is required")
118: 			continue
119: 		case s.UploadBytes < 0 || s.DownloadBytes < 0:
120: 			add("VALIDATION_FAILED", "bytes", "byte counters must not be negative")
121: 			continue
122: 		case !s.EndedAt.After(s.StartedAt):
123: 			add("VALIDATION_FAILED", "sampling", "sample end must be after start")
124: 			continue
125: 		}
126: 		if s.EndedAt.Sub(s.StartedAt) > MaxSampleDuration {
127: 			add("VALIDATION_FAILED", "sampling", "sample interval is longer than the supported maximum")
128: 			continue
129: 		}
130: 		if s.EndedAt.After(now.Add(skew)) {
131: 			add("BATCH_CLOCK_SKEW", "sampling", "sample ends in the future beyond the clock tolerance")
132: 			continue
133: 		}
134: 		if v.NodeServer != nil {
135: 			serverID, exists := v.NodeServer(s.NodeID)
136: 			if !exists {
137: 				add("BATCH_OWNERSHIP_MISMATCH", "node_id", "node does not exist")
138: 				continue
139: 			}
140: 			if v.AgentServerID != "" && serverID != v.AgentServerID {
141: 				add("BATCH_OWNERSHIP_MISMATCH", "node_id", "node belongs to another server")
142: 				continue
143: 			}
144: 		}
145: 		if v.NodeHasHistory != nil && !v.NodeHasHistory(s.NodeID, s.MemberID) {
146: 			add("BATCH_OWNERSHIP_MISMATCH", "member_id", "no credential history for this member on the node")
147: 			continue
148: 		}
149: 		if s.UploadBytes > math.MaxInt64-s.DownloadBytes || total > math.MaxInt64-s.UploadBytes-s.DownloadBytes {
150: 			add("VALIDATION_FAILED", "bytes", "byte counters exceed the signed 64-bit accounting range")
151: 			continue
152: 		}
153: 		total += s.UploadBytes + s.DownloadBytes
154: 	}
155: 	return rejects
156: }
157: 
158: // Allocation is one ledger row.
159: type Allocation struct {
160: 	PeriodStart   time.Time
161: 	PeriodEnd     time.Time
162: 	UpBytes       int64
163: 	DownBytes     int64
164: 	Estimated     bool
165: 	BoundarySplit bool
166: 	Correction    bool
167: }
168: 
169: // Allocate distributes one sample over the candidate cycles. Samples entirely
170: // inside a closed cycle are recorded as corrections; samples straddling a
171: // boundary are split by overlap duration and flagged estimated.
172: func Allocate(sample contracts.UsageSample, windows []period.Boundary, currentStart time.Time) []Allocation {
173: 	slices := period.AllocateSample(sample.StartedAt, sample.EndedAt, windows, sample.UploadBytes, sample.DownloadBytes)
174: 	out := make([]Allocation, 0, len(slices))
175: 	for _, s := range slices {
176: 		out = append(out, Allocation{
177: 			PeriodStart:   s.Period.Start,
178: 			PeriodEnd:     s.Period.End,
179: 			UpBytes:       s.UpBytes,
180: 			DownBytes:     s.DownBytes,
181: 			Estimated:     s.Estimated,
182: 			BoundarySplit: s.Split,
183: 			Correction:    currentStart.IsZero() || s.Period.Start.Before(currentStart),
184: 		})
185: 	}
186: 	return out
187: }
188: 
189: // RejectCodePriority picks the HTTP-relevant code for a set of rejects.
190: func RejectCodePriority(rejects []Reject) string {
191: 	rank := map[string]int{
192: 		"VALIDATION_FAILED":           0,
193: 		"BATCH_CLOCK_SKEW":            1,
194: 		"SNAPSHOT_SCHEMA_UNSUPPORTED": 2,
195: 		"BATCH_OWNERSHIP_MISMATCH":    3,
196: 	}
197: 	best := ""
198: 	for _, r := range rejects {
199: 		if best == "" || rank[r.Code] > rank[best] {
200: 			best = r.Code
201: 		}
202: 	}
203: 	return best
204: }
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
