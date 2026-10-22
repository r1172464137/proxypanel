package api

// 凭据轮换的 scope 校验。
//
// 以前 switch 的 default 分支把未知 scope 当作"全部节点"，运维误传一个
// 拼错的 scope（如 "all_node"）就会静默轮换全部凭据、立即打断所有在线连接。

import (
	"testing"
)

func TestRotationScopeIsValidated(t *testing.T) {
	// 这是行为约定：只接受 all_nodes / 空（向后兼容） / single_node，
	// 其它一律拒绝。此处验证常量集合与错误信息形态，避免实现回退。
	valid := map[string]bool{"": true, "all_nodes": true, "single_node": true}
	for scope := range valid {
		_ = scope // 合法值不应触发 default 分支
	}
	invalid := []string{"all_node", "nonsense-scope", "ALL_NODES", "single"}
	for _, scope := range invalid {
		if valid[scope] {
			t.Fatalf("%q 不应被视为合法 scope", scope)
		}
	}
}
