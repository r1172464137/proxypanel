package protocol

import (
	"encoding/json"
	"strconv"
	"strings"

	"proxypanel/internal/panel/domain"
)

// Sniffing destinations Xray understands. Anything else is rejected instead of
// being written into a config the core will refuse at start-up.
var sniffingDestinations = map[string]bool{
	"http": true, "tls": true, "quic": true, "fakedns": true,
}

var allocateStrategies = map[string]bool{
	"always": true, "random": true, "roundrobin": true, "round-robin": true,
}

// SanitizeInbound validates the inbound-level objects that live beside
// streamSettings/settings: sniffing, allocate and externalProxy. It keeps unknown
// keys untouched (same contract as stream_settings) so a value typed in the JSON
// editor is never silently dropped, but it does reject shapes the core would
// fail on.
func SanitizeInbound(sniffing, allocate domain.Object, externalProxy []any) (domain.Object, domain.Object, []any, []Issue) {
	var issues []Issue
	outSniffing := sniffing
	if outSniffing == nil {
		outSniffing = domain.NewObject()
	}
	outAllocate := allocate
	if outAllocate == nil {
		outAllocate = domain.NewObject()
	}
	outProxy := externalProxy
	if outProxy == nil {
		outProxy = []any{}
	}

	if v, ok := outSniffing.Get("enabled"); ok {
		if _, isBool := asBool(v); !isBool {
			issues = append(issues, Issue{Field: "sniffing.enabled", Message: "must be a boolean"})
		}
	}
	if raw, ok := outSniffing.Get("destOverride"); ok {
		list, err := jsonStrings(raw)
		if err != nil {
			issues = append(issues, Issue{Field: "sniffing.destOverride", Message: "must be an array of strings"})
		} else {
			for _, item := range list {
				if !sniffingDestinations[strings.ToLower(item)] {
					issues = append(issues, Issue{
						Field:   "sniffing.destOverride",
						Message: "unsupported value " + item + " (supported: http, tls, quic, fakedns)",
					})
					break
				}
			}
		}
	}
	for _, key := range []string{"metadataOnly", "routeOnly"} {
		if v, ok := outSniffing.Get(key); ok {
			if _, isBool := asBool(v); !isBool {
				issues = append(issues, Issue{Field: "sniffing." + key, Message: "must be a boolean"})
			}
		}
	}

	if v, ok := outAllocate.String("strategy"); ok && strings.TrimSpace(v) != "" {
		if !allocateStrategies[strings.ToLower(strings.TrimSpace(v))] {
			issues = append(issues, Issue{
				Field:   "allocate.strategy",
				Message: "unsupported strategy " + v + " (supported: always, random, roundrobin)",
			})
		}
	}
	for _, key := range []string{"refresh", "concurrency"} {
		if v, ok := outAllocate.Get(key); ok {
			if n, ok := asInt(v); !ok || n < 0 {
				issues = append(issues, Issue{Field: "allocate." + key, Message: "must be a non-negative integer"})
			}
		}
	}

	for i, entry := range outProxy {
		obj, ok := entry.(map[string]any)
		if !ok {
			issues = append(issues, Issue{
				Field:   "external_proxy",
				Message: "each entry must be an object with forceTls / dest / port / remark",
			})
			break
		}
		dest, hasDest := obj["dest"].(string)
		if !hasDest || strings.TrimSpace(dest) == "" {
			issues = append(issues, Issue{Field: "external_proxy", Message: "entry " + strconv.Itoa(i) + " needs a non-empty dest"})
			break
		}
		port, hasPort := jsonInt(obj["port"])
		if !hasPort || port < 1 || port > 65535 {
			issues = append(issues, Issue{Field: "external_proxy", Message: "entry " + strconv.Itoa(i) + " needs a port between 1 and 65535"})
			break
		}
	}
	return outSniffing, outAllocate, outProxy, issues
}

// ── small JSON helpers ───────────────────────────────────────────────────────
// domain.Object stores raw JSON, so validating a nested value means decoding it.

func asBool(raw json.RawMessage) (bool, bool) {
	var b bool
	if err := json.Unmarshal(raw, &b); err != nil {
		return false, false
	}
	return b, true
}

func jsonStrings(raw json.RawMessage) ([]string, error) {
	var out []string
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// jsonInt accepts any JSON number that is integral (8000 and 8000.0 both work),
// which is what an operator typing into the JSON editor will produce.
func jsonInt(v any) (int, bool) {
	switch n := v.(type) {
	case float64:
		return int(n), n == float64(int(n))
	case json.Number:
		i, err := n.Int64()
		return int(i), err == nil
	case int:
		return n, true
	}
	return 0, false
}

// asInt is jsonInt for a raw JSON value pulled out of a domain.Object.
func asInt(raw json.RawMessage) (int, bool) {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return 0, false
	}
	return jsonInt(v)
}
