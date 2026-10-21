// Package xray validates panel supplied Xray documents, adds the local
// management API and controls the per-instance Xray processes.
package xray

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"proxypanel/internal/contracts"
)

// APITag is the reserved tag of the locally injected management API inbound and
// of the internal routing rule that directs it to the API service.
const (
	APITag    = "agent-api"
	APIListen = "127.0.0.1"
)

// DefaultMaxInstances bounds how many core processes one agent accepts.
const DefaultMaxInstances = 64

var instanceIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

var (
	allowedTopLevel = map[string]struct{}{
		"log": {}, "inbounds": {}, "outbounds": {}, "routing": {}, "policy": {}, "stats": {},
	}
	allowedInboundProtocols = map[string]struct{}{
		"vless": {}, "vmess": {}, "trojan": {}, "shadowsocks": {},
	}
	allowedOutboundProtocols = map[string]struct{}{
		"freedom": {}, "blackhole": {},
	}
	allowedNetworks = map[string]struct{}{
		"tcp": {}, "raw": {}, "kcp": {}, "ws": {}, "grpc": {}, "httpupgrade": {},
		"xhttp": {}, "quic": {}, "http": {},
	}
	allowedSecurities = map[string]struct{}{"none": {}, "tls": {}, "reality": {}}
	allowedLogLevels  = map[string]struct{}{"debug": {}, "info": {}, "warning": {}, "error": {}, "none": {}}
	allowedLogKeys    = map[string]struct{}{"loglevel": {}, "access": {}, "error": {}, "dnsLog": {}, "maskAddress": {}}
)

// ValidationError is returned for panel payloads the agent refuses to run.
type ValidationError struct{ Reason string }

func (e *ValidationError) Error() string { return e.Reason }

func invalid(format string, args ...any) error {
	return &ValidationError{Reason: fmt.Sprintf(format, args...)}
}

// Authorization is the effective local authorization set derived from
// Instance.Users after local expiry filtering.
type Authorization struct {
	ByEmail    map[string]string // client email (as used in the config) -> member id
	ByMember   map[string]string // member id -> client email
	Expired    int
	NextExpiry time.Time
}

// AuthorizationFromUsers drops already expired users and indexes the rest by
// email and by member id so config clients and stats counters can be mapped to
// the stable member id.
func AuthorizationFromUsers(users []contracts.AuthorizedUser, now time.Time) (Authorization, error) {
	auth := Authorization{ByEmail: map[string]string{}, ByMember: map[string]string{}}
	for _, u := range users {
		email := strings.TrimSpace(u.Email)
		member := strings.TrimSpace(u.MemberID)
		if email == "" {
			return Authorization{}, invalid("instance user without email")
		}
		if member == "" {
			return Authorization{}, invalid("instance user %q without member_id", email)
		}
		if prev, ok := auth.ByEmail[email]; ok && prev != member {
			return Authorization{}, invalid("email %q is mapped to member %q and %q", email, prev, member)
		}
		if u.ExpiresAt != nil && !u.ExpiresAt.After(now) {
			auth.Expired++
			delete(auth.ByEmail, email)
			delete(auth.ByMember, member)
			continue
		}
		if prev, ok := auth.ByMember[member]; ok && prev != email {
			return Authorization{}, invalid("member %q is used by two users (%q, %q)", member, prev, email)
		}
		auth.ByEmail[email] = member
		auth.ByMember[member] = email
		if u.ExpiresAt != nil {
			exp := u.ExpiresAt.UTC()
			if auth.NextExpiry.IsZero() || exp.Before(auth.NextExpiry) {
				auth.NextExpiry = exp
			}
		}
	}
	return auth, nil
}

// FilterResult describes what happened while filtering inbound clients.
type FilterResult struct {
	Document       map[string]any
	EmailToMember  map[string]string
	DroppedClients int
	KeptClients    int
	EffectiveUsers []contracts.AuthorizedUser
	InboundCount   int
}

// FilterDocument validates the declared Xray document and keeps only inbound
// clients that are covered by the given authorization. Authorization is applied
// by exact client email; a client email equal to a member id is also accepted so
// panels may use either stable mapping.
func FilterDocument(doc map[string]any, auth Authorization) (FilterResult, error) {
	res := FilterResult{EmailToMember: map[string]string{}}
	out, ok := cloneValue(doc).(map[string]any)
	if !ok {
		return res, invalid("config must be a JSON object")
	}
	inboundsRaw, ok := out["inbounds"]
	if !ok {
		return res, invalid("config must declare inbounds")
	}
	inbounds, ok := inboundsRaw.([]any)
	if !ok {
		return res, invalid("inbounds must be an array")
	}
	if len(inbounds) == 0 {
		return res, invalid("config must declare at least one inbound")
	}
	res.InboundCount = len(inbounds)
	seenTags := map[string]bool{}
	for idx, raw := range inbounds {
		in, ok := raw.(map[string]any)
		if !ok {
			return res, invalid("inbounds[%d] must be an object", idx)
		}
		protocol, _ := in["protocol"].(string)
		if _, ok := allowedInboundProtocols[protocol]; !ok {
			return res, invalid("inbounds[%d]: protocol %q is not allowed", idx, protocol)
		}
		if tag, _ := in["tag"].(string); tag != "" {
			if tag == APITag {
				return res, invalid("inbounds[%d]: tag %q is reserved", idx, APITag)
			}
			if seenTags[tag] {
				return res, invalid("inbounds[%d]: duplicate inbound tag %q", idx, tag)
			}
			seenTags[tag] = true
		}
		if _, err := inboundPort(in); err != nil {
			return res, invalid("inbounds[%d]: %v", idx, err)
		}
		if err := validateStreamSettings(in["streamSettings"], "", false); err != nil {
			return res, invalid("inbounds[%d]: %v", idx, err)
		}
		settings, ok := in["settings"].(map[string]any)
		if !ok {
			return res, invalid("inbounds[%d]: settings must be an object", idx)
		}
		clientsRaw, ok := settings["clients"]
		if !ok {
			return res, invalid("inbounds[%d]: settings.clients is required so per-member authorization can be applied", idx)
		}
		clients, ok := clientsRaw.([]any)
		if !ok {
			return res, invalid("inbounds[%d]: settings.clients must be an array", idx)
		}
		kept := make([]any, 0, len(clients))
		seenEmail := map[string]bool{}
		for cidx, craw := range clients {
			client, ok := craw.(map[string]any)
			if !ok {
				return res, invalid("inbounds[%d].clients[%d] must be an object", idx, cidx)
			}
			email, _ := client["email"].(string)
			email = strings.TrimSpace(email)
			if email == "" {
				return res, invalid("inbounds[%d].clients[%d]: email is required", idx, cidx)
			}
			if seenEmail[email] {
				return res, invalid("inbounds[%d]: duplicate client email %q", idx, email)
			}
			seenEmail[email] = true
			member, ok := auth.ByEmail[email]
			if !ok {
				if mapped, ok2 := auth.ByMember[email]; ok2 {
					member = email
					_ = mapped
				} else {
					res.DroppedClients++
					continue
				}
			}
			res.EmailToMember[email] = member
			kept = append(kept, client)
		}
		settings["clients"] = kept
		in["settings"] = settings
		inbounds[idx] = in
	}
	out["inbounds"] = inbounds
	res.KeptClients = len(res.EmailToMember)
	res.Document = out
	return res, nil
}

// Options controls document validation and injection.
type Options struct {
	APIPort      int
	CertRoot     string
	Now          time.Time
	MaxInstances int
}

// Plan is a validated, ready-to-run instance description.
type Plan struct {
	InstanceID     string
	NodeID         string
	Document       map[string]any // filtered document, no management API
	Config         []byte         // effective configuration including the management API
	Hash           string
	Authorization  Authorization
	EmailToMember  map[string]string
	KeptClients    int
	EffectiveUsers []contracts.AuthorizedUser
	DroppedClients int
	ExpiredUsers   int
	MissingClients int
	NextExpiry     time.Time
}

// Build validates one instance snapshot entry and produces the effective config.
func Build(in contracts.Instance, opts Options) (Plan, error) {
	plan, err := Prepare(in, opts)
	if err != nil {
		return Plan{}, err
	}
	config, err := plan.Render(opts.APIPort)
	if err != nil {
		return Plan{}, err
	}
	plan.Config = config
	return plan, nil
}

// Render injects the local management API into the filtered document.
func (p Plan) Render(apiPort int) ([]byte, error) {
	return BuildEffective(p.Document, apiPort)
}

// Prepare validates and filters one instance snapshot entry. It does not need a
// management API port, so callers can compare it with the running state first.
func Prepare(in contracts.Instance, opts Options) (Plan, error) {
	if opts.Now.IsZero() {
		opts.Now = time.Now().UTC()
	}
	if err := ValidateIDs(in.ID, in.NodeID); err != nil {
		return Plan{}, err
	}
	doc, err := DecodeDocument(in.Config)
	if err != nil {
		return Plan{}, err
	}
	if err := validateTopLevel(doc); err != nil {
		return Plan{}, err
	}
	if err := validateOutbounds(doc); err != nil {
		return Plan{}, err
	}
	if err := validateRouting(doc); err != nil {
		return Plan{}, err
	}
	if err := validateLog(doc); err != nil {
		return Plan{}, err
	}
	auth, err := AuthorizationFromUsers(in.Users, opts.Now)
	if err != nil {
		return Plan{}, err
	}
	filtered, err := FilterDocument(doc, auth)
	if err != nil {
		return Plan{}, err
	}
	// Re-check certificate paths with the real certificate root now known.
	inbounds := filtered.Document["inbounds"].([]any)
	for idx, raw := range inbounds {
		in := raw.(map[string]any)
		if err := validateStreamSettings(in["streamSettings"], opts.CertRoot, true); err != nil {
			return Plan{}, invalid("inbounds[%d]: %v", idx, err)
		}
	}
	hash, err := HashDocument(filtered.Document)
	if err != nil {
		return Plan{}, err
	}
	plan := Plan{
		InstanceID:     in.ID,
		NodeID:         in.NodeID,
		Document:       filtered.Document,
		Hash:           hash,
		Authorization:  auth,
		EmailToMember:  filtered.EmailToMember,
		KeptClients:    filtered.KeptClients,
		DroppedClients: filtered.DroppedClients,
		ExpiredUsers:   auth.Expired,
		NextExpiry:     auth.NextExpiry,
	}
	for email := range auth.ByEmail {
		if _, ok := filtered.EmailToMember[email]; !ok {
			if _, ok2 := filtered.EmailToMember[auth.ByEmail[email]]; !ok2 {
				plan.MissingClients++
			}
		}
	}
	for _, u := range in.Users {
		member := strings.TrimSpace(u.MemberID)
		for _, kept := range filtered.EmailToMember {
			if kept == member {
				plan.EffectiveUsers = append(plan.EffectiveUsers, contracts.AuthorizedUser{
					MemberID: member, Email: strings.TrimSpace(u.Email), ExpiresAt: u.ExpiresAt,
				})
				break
			}
		}
	}

	return plan, nil
}

// ValidateIDs enforces the instance id rules: the id is the node id and is safe
// to use as a directory name.
func ValidateIDs(id, nodeID string) error {
	if !instanceIDPattern.MatchString(id) {
		return invalid("instance id %q must match %s", id, instanceIDPattern.String())
	}
	if id != nodeID {
		return invalid("instance id %q must equal node_id %q", id, nodeID)
	}
	if id == "." || id == ".." {
		return invalid("instance id %q is not a safe directory name", id)
	}
	return nil
}

// ValidateSnapshot checks the envelope level rules that the agent can verify
// locally before touching any running instance.
func ValidateSnapshot(s contracts.Snapshot, maxInstances int) error {
	if s.SchemaVersion != contracts.SchemaVersion {
		return invalid("snapshot schema_version %q is not supported (expected %q)", s.SchemaVersion, contracts.SchemaVersion)
	}
	if strings.TrimSpace(s.ControlGeneration) == "" {
		return invalid("snapshot control_generation is empty")
	}
	if s.Revision < 1 {
		return invalid("snapshot revision %d is invalid", s.Revision)
	}
	if maxInstances <= 0 {
		maxInstances = DefaultMaxInstances
	}
	if len(s.Instances) > maxInstances {
		return invalid("snapshot contains %d instances, limit is %d", len(s.Instances), maxInstances)
	}
	seen := map[string]bool{}
	total := 0
	for idx, inst := range s.Instances {
		if err := ValidateIDs(inst.ID, inst.NodeID); err != nil {
			return invalid("instances[%d]: %v", idx, err)
		}
		if seen[inst.ID] {
			return invalid("instances[%d]: duplicate instance id %q", idx, inst.ID)
		}
		seen[inst.ID] = true
		if len(inst.Config) == 0 {
			return invalid("instances[%d]: config is empty", idx)
		}
		emails := map[string]bool{}
		members := map[string]bool{}
		for uidx, u := range inst.Users {
			email := strings.TrimSpace(u.Email)
			if email == "" {
				return invalid("instances[%d].users[%d]: email is required", idx, uidx)
			}
			if strings.TrimSpace(u.MemberID) == "" {
				return invalid("instances[%d].users[%d]: member_id is required", idx, uidx)
			}
			if emails[email] {
				return invalid("instances[%d].users[%d]: duplicate email %q", idx, uidx, email)
			}
			if members[u.MemberID] {
				return invalid("instances[%d].users[%d]: duplicate member_id %q", idx, uidx, u.MemberID)
			}
			emails[email] = true
			members[u.MemberID] = true
		}
		total += len(inst.Users)
	}
	if s.ExpectedUserCount != total {
		return invalid("snapshot expected_user_count %d does not match the %d users listed", s.ExpectedUserCount, total)
	}
	if s.IssuedAt.IsZero() {
		return invalid("snapshot issued_at is zero")
	}
	return nil
}

// DecodeDocument decodes a raw Xray document preserving numeric literals.
func DecodeDocument(raw []byte) (map[string]any, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, invalid("config is empty")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var doc map[string]any
	if err := dec.Decode(&doc); err != nil {
		return nil, invalid("config is not a JSON object: %v", err)
	}
	if doc == nil {
		return nil, invalid("config must be a JSON object")
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return nil, invalid("config has trailing content")
	}
	return doc, nil
}

// HashDocument returns a stable hash of a document (independent of member order
// or formatting) used to decide whether an instance must be restarted.
func HashDocument(doc map[string]any) (string, error) {
	raw, err := json.Marshal(doc)
	if err != nil {
		return "", fmt.Errorf("hash config: %v", err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// BuildEffective injects the local management API, stats, policy flags and the
// routing rule that serves the API. Everything else is the panel document.
func BuildEffective(doc map[string]any, apiPort int) ([]byte, error) {
	if apiPort <= 0 || apiPort > 65535 {
		return nil, fmt.Errorf("management API port %d is invalid", apiPort)
	}
	clone, ok := cloneValue(doc).(map[string]any)
	if !ok {
		return nil, invalid("config must be a JSON object")
	}
	inboundsRaw, ok := clone["inbounds"].([]any)
	if !ok || len(inboundsRaw) == 0 {
		return nil, invalid("config must declare at least one inbound")
	}
	for idx, raw := range inboundsRaw {
		in, ok := raw.(map[string]any)
		if !ok {
			return nil, invalid("inbounds[%d] must be an object", idx)
		}
		port, err := inboundPort(in)
		if err != nil {
			return nil, invalid("inbounds[%d]: %v", idx, err)
		}
		if port == apiPort {
			return nil, invalid("inbounds[%d] uses the reserved management API port %d", idx, apiPort)
		}
	}
	clone["api"] = map[string]any{"tag": APITag, "services": []any{"StatsService"}}
	clone["stats"] = map[string]any{}

	policy, _ := clone["policy"].(map[string]any)
	if policy == nil {
		policy = map[string]any{}
	}
	system, _ := policy["system"].(map[string]any)
	if system == nil {
		system = map[string]any{}
	}
	system["statsInboundUplink"] = true
	system["statsInboundDownlink"] = true
	system["statsUserUplink"] = true
	system["statsUserDownlink"] = true
	policy["system"] = system
	// Xray 26.3.27 only emits user>>>email>>>traffic counters when the policy
	// level used by the client enables them, not just policy.system.
	levels := map[string]any{}
	if existing, ok := policy["levels"].(map[string]any); ok {
		for k, v := range existing {
			levels[k] = v
		}
	}
	for _, level := range clientLevels(inboundsRaw) {
		entry, _ := levels[level].(map[string]any)
		merged := map[string]any{}
		for k, v := range entry {
			merged[k] = v
		}
		merged["statsUserUplink"] = true
		merged["statsUserDownlink"] = true
		levels[level] = merged
	}
	if len(levels) > 0 {
		policy["levels"] = levels
	}
	clone["policy"] = policy

	routing, _ := clone["routing"].(map[string]any)
	if routing == nil {
		routing = map[string]any{}
	}
	rules, _ := routing["rules"].([]any)
	apiRule := map[string]any{"type": "field", "inboundTag": []any{APITag}, "outboundTag": APITag}
	merged := make([]any, 0, len(rules)+1)
	merged = append(merged, apiRule)
	merged = append(merged, rules...)
	routing["rules"] = merged
	clone["routing"] = routing

	apiInbound := map[string]any{
		"tag":      APITag,
		"listen":   APIListen,
		"port":     json.Number(strconv.Itoa(apiPort)),
		"protocol": "dokodemo-door",
		"settings": map[string]any{"address": APIListen, "network": "tcp,udp"},
	}
	inbounds := make([]any, 0, len(inboundsRaw)+1)
	inbounds = append(inbounds, inboundsRaw...)
	inbounds = append(inbounds, apiInbound)
	clone["inbounds"] = inbounds

	out, err := json.MarshalIndent(clone, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode effective config: %v", err)
	}
	return out, nil
}

// RefilterClients rebuilds a previously applied document with the latest
// authorization: clients that are no longer authorized are removed, authorized
// clients keep their existing credentials. It is used when a new document is
// rejected, so a technical rollback never restores revoked users.
func RefilterClients(doc map[string]any, auth Authorization) (FilterResult, error) {
	return FilterDocument(doc, auth)
}

// clientLevels returns the policy levels referenced by inbound clients (level 0
// is the Xray default).
func clientLevels(inbounds []any) []string {
	seen := map[string]bool{"0": true}
	for _, raw := range inbounds {
		in, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		settings, ok := in["settings"].(map[string]any)
		if !ok {
			continue
		}
		clients, ok := settings["clients"].([]any)
		if !ok {
			continue
		}
		for _, craw := range clients {
			c, ok := craw.(map[string]any)
			if !ok {
				continue
			}
			if n, ok := asInt(c["level"]); ok {
				seen[strconv.FormatInt(n, 10)] = true
			}
		}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool {
		a, _ := strconv.Atoi(out[i])
		b, _ := strconv.Atoi(out[j])
		return a < b
	})
	return out
}

func validateTopLevel(doc map[string]any) error {
	keys := make([]string, 0, len(doc))
	for k := range doc {
		keys = append(keys, k)
	}
	for _, k := range keys {
		if _, ok := allowedTopLevel[k]; !ok {
			return invalid("top-level key %q is not allowed by the agent (allowed: log, inbounds, outbounds, routing, policy, stats)", k)
		}
	}
	return nil
}

func validateOutbounds(doc map[string]any) error {
	raw, ok := doc["outbounds"]
	if !ok {
		return nil
	}
	list, ok := raw.([]any)
	if !ok {
		return invalid("outbounds must be an array")
	}
	for idx, r := range list {
		ob, ok := r.(map[string]any)
		if !ok {
			return invalid("outbounds[%d] must be an object", idx)
		}
		protocol, _ := ob["protocol"].(string)
		if _, ok := allowedOutboundProtocols[protocol]; !ok {
			return invalid("outbounds[%d]: protocol %q is not allowed (only freedom and blackhole)", idx, protocol)
		}
		if tag, _ := ob["tag"].(string); tag == APITag {
			return invalid("outbounds[%d]: tag %q is reserved", idx, APITag)
		}
	}
	return nil
}

func validateRouting(doc map[string]any) error {
	raw, ok := doc["routing"]
	if !ok {
		return nil
	}
	routing, ok := raw.(map[string]any)
	if !ok {
		return invalid("routing must be an object")
	}
	if _, ok := routing["balancers"]; ok {
		return invalid("routing.balancers is not allowed by the agent")
	}
	rulesRaw, ok := routing["rules"]
	if !ok {
		return nil
	}
	rules, ok := rulesRaw.([]any)
	if !ok {
		return invalid("routing.rules must be an array")
	}
	for idx, r := range rules {
		rule, ok := r.(map[string]any)
		if !ok {
			return invalid("routing.rules[%d] must be an object", idx)
		}
		if _, ok := rule["balancerTag"]; ok {
			return invalid("routing.rules[%d]: balancerTag is not allowed by the agent", idx)
		}
		if t, _ := rule["type"].(string); t != "" && t != "field" {
			return invalid("routing.rules[%d]: rule type %q is not supported", idx, t)
		}
		if tagsRaw, ok := rule["inboundTag"]; ok {
			tags, ok := tagsRaw.([]any)
			if !ok {
				return invalid("routing.rules[%d]: inboundTag must be an array", idx)
			}
			for _, t := range tags {
				if s, _ := t.(string); s == APITag {
					return invalid("routing.rules[%d]: inboundTag %q is reserved", idx, APITag)
				}
			}
		}
		if out, _ := rule["outboundTag"].(string); out == APITag {
			return invalid("routing.rules[%d]: outboundTag %q is reserved", idx, APITag)
		}
	}
	return nil
}

func validateLog(doc map[string]any) error {
	raw, ok := doc["log"]
	if !ok {
		doc["log"] = map[string]any{"loglevel": "warning", "access": "none"}
		return nil
	}
	logDoc, ok := raw.(map[string]any)
	if !ok {
		return invalid("log must be an object")
	}
	level := "warning"
	for key, val := range logDoc {
		if _, ok := allowedLogKeys[key]; !ok {
			return invalid("log.%s is not allowed by the agent", key)
		}
		if key == "loglevel" {
			s, _ := val.(string)
			if s == "" {
				continue
			}
			if _, ok := allowedLogLevels[s]; !ok {
				return invalid("log.loglevel %q is not supported", s)
			}
			level = s
		}
	}
	// Never let the panel point core logs at arbitrary files; the access log is
	// also not used for accounting.
	doc["log"] = map[string]any{"loglevel": level, "access": "none"}
	return nil
}

func validateStreamSettings(raw any, certRoot string, checkCertPaths bool) error {
	if raw == nil {
		return nil
	}
	stream, ok := raw.(map[string]any)
	if !ok {
		return invalid("streamSettings must be an object")
	}
	if v, ok := stream["network"]; ok {
		s, _ := v.(string)
		if s != "" {
			if _, ok := allowedNetworks[s]; !ok {
				return invalid("streamSettings.network %q is not supported", s)
			}
		}
	}
	security := ""
	if v, ok := stream["security"]; ok {
		s, _ := v.(string)
		if s != "" {
			if _, ok := allowedSecurities[s]; !ok {
				return invalid("streamSettings.security %q is not supported", s)
			}
			security = s
		}
	}
	if security == "tls" && checkCertPaths {
		if tlsRaw, ok := stream["tlsSettings"]; ok {
			if err := validateCertificatePaths(tlsRaw, certRoot); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateCertificatePaths(raw any, certRoot string) error {
	tlsDoc, ok := raw.(map[string]any)
	if !ok {
		return invalid("tlsSettings must be an object")
	}
	if err := checkCertPath(tlsDoc, "certificateFile", certRoot); err != nil {
		return err
	}
	if err := checkCertPath(tlsDoc, "keyFile", certRoot); err != nil {
		return err
	}
	if certsRaw, ok := tlsDoc["certificates"]; ok {
		certs, ok := certsRaw.([]any)
		if !ok {
			return invalid("tlsSettings.certificates must be an array")
		}
		for idx, c := range certs {
			cert, ok := c.(map[string]any)
			if !ok {
				return invalid("tlsSettings.certificates[%d] must be an object", idx)
			}
			if err := checkCertPath(cert, "certificateFile", certRoot); err != nil {
				return err
			}
			if err := checkCertPath(cert, "keyFile", certRoot); err != nil {
				return err
			}
		}
	}
	return nil
}

func checkCertPath(doc map[string]any, key, certRoot string) error {
	v, ok := doc[key]
	if !ok {
		return nil
	}
	s, _ := v.(string)
	if strings.TrimSpace(s) == "" {
		return invalid("tlsSettings.%s must not be empty", key)
	}
	if !filepath.IsAbs(s) {
		return invalid("tlsSettings.%s %q must be an absolute path inside the agent certificate directory", key, s)
	}
	if strings.Contains(s, "..") {
		return invalid("tlsSettings.%s %q must not contain path traversal", key, s)
	}
	if certRoot == "" {
		return invalid("tlsSettings.%s %q is not allowed: no certificate directory is configured", key, s)
	}
	clean := filepath.Clean(s)
	rel, err := filepath.Rel(filepath.Clean(certRoot), clean)
	if err != nil {
		return invalid("tlsSettings.%s %q is outside the agent certificate directory", key, s)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return invalid("tlsSettings.%s %q is outside the agent certificate directory", key, s)
	}
	return nil
}

func inboundPort(in map[string]any) (int, error) {
	v, ok := in["port"]
	if !ok {
		return 0, invalid("port is required")
	}
	n, ok := asInt(v)
	if !ok {
		return 0, invalid("port must be a number")
	}
	if n < 1 || n > 65535 {
		return 0, invalid("port %d is out of range", n)
	}
	return int(n), nil
}

func asInt(v any) (int64, bool) {
	switch t := v.(type) {
	case json.Number:
		n, err := t.Int64()
		if err != nil {
			f, ferr := t.Float64()
			if ferr != nil {
				return 0, false
			}
			return int64(f), true
		}
		return n, true
	case float64:
		return int64(t), true
	case int:
		return int64(t), true
	case int64:
		return t, true
	}
	return 0, false
}

func cloneValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, vv := range t {
			out[k] = cloneValue(vv)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, vv := range t {
			out[i] = cloneValue(vv)
		}
		return out
	default:
		return v
	}
}
