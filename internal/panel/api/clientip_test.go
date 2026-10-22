package api

import (
	"net/http/httptest"
	"testing"
)

// 限流键的取值规则。
//
// 面板在 nginx 之后时 RemoteAddr 恒为 127.0.0.1，直接当键会导致全站共享
// 10 次/分钟的桶。但也不能无条件相信 X-Forwarded-For，否则客户端自带该头
// 就能绕过限流。规则：只有直连方是可信代理（回环/私有网段）时才采信 XFF。

func TestClientIPPrefersRemoteWhenDirect(t *testing.T) {
	r := httptest.NewRequest("POST", "/api/v1/auth/login", nil)
	r.RemoteAddr = "203.0.113.9:5555"
	// 外部直连时声称自己是别人：必须忽略，否则限流可被绕过
	r.Header.Set("X-Forwarded-For", "198.51.100.7")
	if got := clientIP(r); got != "203.0.113.9" {
		t.Fatalf("直连客户端不得采信 XFF，got %q", got)
	}
}

func TestClientIPUsesXFFBehindTrustedProxy(t *testing.T) {
	cases := []struct{ remote, xff, want string }{
		{"127.0.0.1:4000", "198.51.100.7", "198.51.100.7"},          // 容器内 nginx
		{"10.1.2.3:4000", "198.51.100.7", "198.51.100.7"},           // 私有网段反代
		{"172.20.32.1:4000", "198.51.100.7", "198.51.100.7"},        // docker bridge
		{"127.0.0.1:4000", "1.1.1.1, 198.51.100.7", "198.51.100.7"}, // 多跳取最右
		{"127.0.0.1:4000", "", "127.0.0.1"},                         // 无 XFF 时回落
		{"127.0.0.1:4000", "not-an-ip", "127.0.0.1"},                // 非法值回落
	}
	for _, tc := range cases {
		r := httptest.NewRequest("POST", "/api/v1/auth/login", nil)
		r.RemoteAddr = tc.remote
		if tc.xff != "" {
			r.Header.Set("X-Forwarded-For", tc.xff)
		}
		if got := clientIP(r); got != tc.want {
			t.Fatalf("remote=%s xff=%q -> %q, want %q", tc.remote, tc.xff, got, tc.want)
		}
	}
}

func TestIsTrustedProxy(t *testing.T) {
	trusted := []string{"127.0.0.1", "::1", "10.0.0.1", "192.168.1.1", "172.16.0.1", "169.254.1.1"}
	for _, h := range trusted {
		if !isTrustedProxy(h) {
			t.Fatalf("%s 应视为可信代理", h)
		}
	}
	untrusted := []string{"203.0.113.9", "8.8.8.8", "not-an-ip", ""}
	for _, h := range untrusted {
		if isTrustedProxy(h) {
			t.Fatalf("%s 不应视为可信代理", h)
		}
	}
}
