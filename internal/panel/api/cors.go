package api

import (
	"net/http"
	"strings"
)

// CORS 支持：让前端与后端可以部署在不同 Origin 上。
//
// 默认**不启用**（白名单为空），因此同源部署的行为与之前完全一致，零开销。
// 只有显式配置 PANEL_ALLOWED_ORIGINS 才会加 CORS 头。
//
// 两条必须遵守的规则：
//  1. 带凭据（Cookie）的跨域请求，Access-Control-Allow-Origin **不能是 `*`**，
//     必须是精确回显请求的 Origin，否则浏览器会拒绝整个响应。
//  2. 必须带 Vary: Origin，否则中间缓存会把 A 站的 CORS 头复用给 B 站。
type corsConfig struct {
	// allowed 是规范化后的 Origin 白名单（协议+主机+端口，无尾斜杠）。
	allowed map[string]bool
}

// parseAllowedOrigins 解析逗号分隔的白名单。空字符串表示不启用 CORS。
func parseAllowedOrigins(raw string) corsConfig {
	cfg := corsConfig{allowed: map[string]bool{}}
	for _, part := range strings.Split(raw, ",") {
		origin := strings.TrimSpace(part)
		if origin == "" {
			continue
		}
		// 统一去掉尾部斜杠，避免 "https://a.com/" 与 "https://a.com" 不匹配。
		origin = strings.TrimRight(origin, "/")
		// 明确拒绝通配符：带凭据时使用 `*` 会被浏览器拒绝，静默失效更难排查。
		if origin == "*" {
			continue
		}
		cfg.allowed[origin] = true
	}
	return cfg
}

func (c corsConfig) enabled() bool { return len(c.allowed) > 0 }

// allows 判断该 Origin 是否在白名单内。
func (c corsConfig) allows(origin string) bool {
	if origin == "" {
		return false
	}
	return c.allowed[strings.TrimRight(origin, "/")]
}

// middleware 包一层 CORS 处理。
//
// 未启用时直接透传（不产生任何额外头、不读请求头），保证同源部署零影响。
func (c corsConfig) middleware(next http.Handler) http.Handler {
	if !c.enabled() {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		// 无论是否匹配都要声明 Vary，否则缓存可能把别的 Origin 的响应吐给本站。
		w.Header().Add("Vary", "Origin")

		if c.allows(origin) {
			// 精确回显，不能是 "*"（带 Cookie 时浏览器会拒绝）。
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Credentials", "true")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
			// X-CSRF-Token 与 Idempotency-Key 是自定义头，必须显式允许，
			// 否则写请求的预检会失败。
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-CSRF-Token, Idempotency-Key")
			w.Header().Set("Access-Control-Max-Age", "600")
		}

		// 预检请求只回 CORS 头，不进入业务逻辑与鉴权。
		// 不在白名单内的 Origin 得到 204 但无 CORS 头，浏览器会自行拒绝。
		if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}
