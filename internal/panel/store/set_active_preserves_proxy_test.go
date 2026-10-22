package store

// D6 回归：批量停用不得改动 proxy_enabled。
//
// 修复前 batch_submit.go 里有：
//     if !active { m.ProxyEnabled = false }
// 但恢复 active=true 时没有任何对应逻辑还原它。后果是批量停用一批人再启用，
// 这些人会永久处于 proxy_paused（订阅输出 403），界面也不提示，只能逐个手动
// 重开代理开关。
//
// 同一个"停用"在两条路径上语义还不一致：单成员 PATCH 从来不碰 proxy_enabled。
// 撤权职责属于 proxy_enabled 字段本身（有独立开关），不该由 active 隐式代办。

import (
	"context"
	"testing"

	"proxypanel/internal/panel/domain"
)

func TestBatchSetActiveDoesNotTouchProxyEnabled(t *testing.T) {
	st := newRegStore(t)
	ctx := context.Background()

	mbr := mustCreateMemberForBatch(t, st, "d6-user")

	// 停用：先预览再提交（真实流程）
	if err := submitSetActive(ctx, st, mbr, false, "d6-off"); err != nil {
		t.Fatalf("submit active=false: %v", err)
	}

	var active, proxyEnabled bool
	if err := st.DB.QueryRowContext(ctx,
		`SELECT active, proxy_enabled FROM members WHERE id=$1`, mbr).Scan(&active, &proxyEnabled); err != nil {
		t.Fatalf("read member: %v", err)
	}
	if active {
		t.Fatal("停用后 active 应为 false")
	}
	if !proxyEnabled {
		t.Fatal("停用不应改动 proxy_enabled（D6）：这是一条独立开关，改动后无法还原")
	}

	// 重新启用后仍然保持 true
	if err := submitSetActive(ctx, st, mbr, true, "d6-on"); err != nil {
		t.Fatalf("submit active=true: %v", err)
	}
	var active2, proxy2 bool
	if err := st.DB.QueryRowContext(ctx,
		`SELECT active, proxy_enabled FROM members WHERE id=$1`, mbr).Scan(&active2, &proxy2); err != nil {
		t.Fatal(err)
	}
	if !active2 || !proxy2 {
		t.Fatalf("重新启用后应 active=true proxy_enabled=true，实际 active=%v proxy=%v", active2, proxy2)
	}
}

// 单成员路径本来就不改 proxy_enabled，这里锁住两条路径的一致性。
func TestUpdateMemberActiveDoesNotTouchProxyEnabled(t *testing.T) {
	st := newRegStore(t)
	ctx := context.Background()
	mbr := mustCreateMemberForBatch(t, st, "d6-single")

	if _, err := st.UpdateMember(ctx, mbr, 0, func(m *domain.Member) error {
		m.Active = false
		return nil
	}); err != nil {
		t.Fatalf("UpdateMember: %v", err)
	}
	var proxy bool
	if err := st.DB.QueryRowContext(ctx, `SELECT proxy_enabled FROM members WHERE id=$1`, mbr).Scan(&proxy); err != nil {
		t.Fatal(err)
	}
	if !proxy {
		t.Fatal("单成员停用不应关闭 proxy_enabled")
	}
}

// submitSetActive 走真实的两步流程：CreatePreview → SubmitBatch。
func submitSetActive(ctx context.Context, st *Store, memberID string, active bool, key string) error {
	values := domain.NewObject()
	_ = values.Set("active", active)
	preview, err := st.CreatePreview(ctx, PreviewRequest{
		Operation: OpSetActive,
		MemberIDs: []string{memberID},
		Values:    values,
		CreatedBy: "test",
	})
	if err != nil {
		return err
	}
	_, err = st.SubmitBatch(ctx, BatchSubmitRequest{
		PreviewID:      preview.ID,
		IdempotencyKey: key,
		Confirm:        true,
		CreatedBy:      "test",
	})
	return err
}

// mustCreateMemberForBatch 直接插入一条可被批量操作的成员。
func mustCreateMemberForBatch(t *testing.T, st *Store, name string) string {
	t.Helper()
	id := "mbr_" + name
	if _, err := st.DB.ExecContext(context.Background(), `INSERT INTO members
		(id, username, password_hash, role, active, proxy_enabled)
		VALUES ($1,$2,'x','member',true,true)`, id, name); err != nil {
		t.Fatalf("insert member: %v", err)
	}
	return id
}
