package reporting

import (
	"proxypanel/internal/agent/state"
	"proxypanel/internal/agent/xray"
)

// CounterItems maps raw Xray counters to member deltas.
func CounterItems(counters []xray.StatCounter, emailToMember map[string]string) ([]state.CounterItem, int) {
	items := make([]state.CounterItem, 0, len(counters))
	unmapped := 0
	for _, c := range counters {
		member, ok := MemberForCounter(c.Name, emailToMember)
		if !ok {
			unmapped++
			continue
		}
		items = append(items, state.CounterItem{MemberID: member, Counter: c.Name, Value: c.Value})
	}
	return items, unmapped
}
