package main

import (
	"net/http"
	"testing"
)

// PANEL_COOKIE_SAMESITE 的解析与校验。
//
// 关键点：SameSite=None 必须配 Secure。浏览器会**静默丢弃**不满足该组合的
// Cookie，表现为"登录返回 200 但下一个请求 401"。所以这里让配置错误直接导致
// 启动失败，而不是留一个线上幽灵问题。

func TestCookieSameSiteDefaultsToStrict(t *testing.T) {
	t.Setenv("PANEL_COOKIE_SAMESITE", "")
	got, err := cookieSameSiteFromEnv(false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// 默认必须与改造前一致（Strict），否则刷新页面就可能掉登录态
	if got != http.SameSiteStrictMode {
		t.Fatalf("default = %v, want SameSiteStrictMode", got)
	}
}

func TestCookieSameSiteParsesValues(t *testing.T) {
	cases := map[string]http.SameSite{
		"strict": http.SameSiteStrictMode,
		"lax":    http.SameSiteLaxMode,
		"NONE":   http.SameSiteNoneMode, // 大小写不敏感
		" none ": http.SameSiteNoneMode, // 前后空格容忍
	}
	for raw, want := range cases {
		t.Setenv("PANEL_COOKIE_SAMESITE", raw)
		got, err := cookieSameSiteFromEnv(false)
		if err != nil {
			t.Fatalf("%q: unexpected error: %v", raw, err)
		}
		if got != want {
			t.Fatalf("%q = %v, want %v", raw, got, want)
		}
	}
}

func TestCookieSameSiteNoneRequiresSecure(t *testing.T) {
	t.Setenv("PANEL_COOKIE_SAMESITE", "none")
	// dev 模式下 Cookie 不带 Secure，此时 SameSite=None 会被浏览器丢弃
	if _, err := cookieSameSiteFromEnv(true); err == nil {
		t.Fatal("SameSite=None 配 PANEL_DEV=true 必须启动失败")
	}
	// 非 dev（Secure=true）才允许
	if _, err := cookieSameSiteFromEnv(false); err != nil {
		t.Fatalf("非 dev 下 SameSite=None 应当允许: %v", err)
	}
}

func TestCookieSameSiteRejectsUnknown(t *testing.T) {
	t.Setenv("PANEL_COOKIE_SAMESITE", "bogus")
	if _, err := cookieSameSiteFromEnv(false); err == nil {
		t.Fatal("未知取值必须报错，而不是静默回落到默认值")
	}
}
