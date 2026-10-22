package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// 前后端分离部署的 CORS 行为。默认必须完全关闭，否则同源部署会多出无意义的头，
// 且可能被中间缓存误用。

func TestParseAllowedOrigins(t *testing.T) {
	cases := []struct {
		raw    string
		origin string
		want   bool
	}{
		{"", "https://panel.example.com", false}, // 未配置 → 不启用
		{"https://panel.example.com", "https://panel.example.com", true},
		{"https://panel.example.com/", "https://panel.example.com", true}, // 尾斜杠被裁
		{" https://a.com , https://b.com ", "https://b.com", true},        // 空格与多值
		{"https://a.com", "https://evil.com", false},
		{"*", "https://a.com", false},                 // 通配符被拒绝
		{"https://a.com", "https://a.com:443", false}, // 端口不同不算同源
	}
	for _, tc := range cases {
		cfg := parseAllowedOrigins(tc.raw)
		if got := cfg.allows(tc.origin); got != tc.want {
			t.Fatalf("parseAllowedOrigins(%q).allows(%q) = %v, want %v",
				tc.raw, tc.origin, got, tc.want)
		}
	}
}

func TestCORSDisabledByDefault(t *testing.T) {
	called := false
	h := parseAllowedOrigins("").middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/members", nil)
	req.Header.Set("Origin", "https://panel.example.com")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if !called {
		t.Fatal("未配置白名单时请求必须原样透传")
	}
	// 关键：一个 CORS 头都不能有，否则同源部署的行为就变了
	for _, h := range []string{"Access-Control-Allow-Origin", "Access-Control-Allow-Credentials", "Vary"} {
		if v := rec.Header().Get(h); v != "" {
			t.Fatalf("未启用 CORS 时不应出现 %s，got %q", h, v)
		}
	}
}

func TestCORSAllowsWhitelistedOrigin(t *testing.T) {
	h := parseAllowedOrigins("https://panel.example.com").middleware(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/members", nil)
	req.Header.Set("Origin", "https://panel.example.com")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	// 必须是精确回显，不能是 "*"（带 Credentials 时浏览器会拒绝整个响应）
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://panel.example.com" {
		t.Fatalf("Allow-Origin = %q, want the exact origin", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Credentials"); got != "true" {
		t.Fatalf("Allow-Credentials = %q, want true", got)
	}
	// Vary: Origin 防止中间缓存把 A 站的 CORS 头复用给 B 站
	if got := rec.Header().Get("Vary"); got != "Origin" {
		t.Fatalf("Vary = %q, want Origin", got)
	}
	// 自定义头必须允许，否则写请求的预检会失败
	allowed := rec.Header().Get("Access-Control-Allow-Headers")
	for _, need := range []string{"X-CSRF-Token", "Idempotency-Key"} {
		if !containsHeader(allowed, need) {
			t.Fatalf("Allow-Headers 缺少 %s，got %q", need, allowed)
		}
	}
}

func TestCORSRejectsForeignOrigin(t *testing.T) {
	h := parseAllowedOrigins("https://panel.example.com").middleware(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/members", nil)
	req.Header.Set("Origin", "https://evil.example.net")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("非白名单 Origin 不得返回 Allow-Origin，got %q", got)
	}
	// Vary 仍然要加（缓存不能把"无 CORS 头"的响应复用到白名单站点）
	if got := rec.Header().Get("Vary"); got != "Origin" {
		t.Fatalf("Vary = %q, want Origin", got)
	}
}

func TestCORSPreflightShortCircuits(t *testing.T) {
	reached := false
	h := parseAllowedOrigins("https://panel.example.com").middleware(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			reached = true
			w.WriteHeader(http.StatusOK)
		}))

	req := httptest.NewRequest(http.MethodOptions, "/api/v1/admin/members", nil)
	req.Header.Set("Origin", "https://panel.example.com")
	req.Header.Set("Access-Control-Request-Method", "PATCH")
	req.Header.Set("Access-Control-Request-Headers", "X-CSRF-Token")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if reached {
		t.Fatal("预检请求不应进入业务处理器（否则会先撞 CSRF 校验）")
	}
	if rec.Code != http.StatusNoContent {
		t.Fatalf("预检应返回 204，got %d", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://panel.example.com" {
		t.Fatalf("预检也必须返回 Allow-Origin，got %q", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Methods"); got == "" {
		t.Fatal("预检必须声明 Allow-Methods")
	}
}

func containsHeader(list, want string) bool {
	for _, part := range splitComma(list) {
		if part == want {
			return true
		}
	}
	return false
}

func splitComma(s string) []string {
	var out []string
	cur := ""
	for _, r := range s {
		if r == ',' {
			out = append(out, trimSpace(cur))
			cur = ""
			continue
		}
		cur += string(r)
	}
	out = append(out, trimSpace(cur))
	return out
}

func trimSpace(s string) string {
	start, end := 0, len(s)
	for start < end && (s[start] == ' ' || s[start] == '\t') {
		start++
	}
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t') {
		end--
	}
	return s[start:end]
}
