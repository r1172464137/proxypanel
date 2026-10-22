package store

// 订阅删除的语义回归。
//
// DeleteSubscription 以前是软删（只置 Archived=true），造成三个问题：
//   - 界面按钮叫"删除"、确认框也写"将删除"，但数据还在；
//   - 归档订阅仍出现在"目标订阅"下拉里，可被选为新绑定目标；
//   - 归档订阅仍占用节点组引用，使节点组永远删不掉且无入口清理。
//
// 现在改为真删除，由数据库外键保证级联与拒绝语义。

import (
	"context"
	"strings"
	"testing"

	"proxypanel/internal/panel/domain"
)

func TestDeleteSubscriptionRemovesTheRow(t *testing.T) {
	st := newRegStore(t)
	ctx := context.Background()

	sub := mustCreateSubscription(t, st, "del-target")
	if _, err := st.DeleteSubscription(ctx, sub.ID); err != nil {
		t.Fatalf("DeleteSubscription: %v", err)
	}
	if _, err := st.GetSubscription(ctx, sub.ID); err == nil {
		t.Fatal("删除后不应还能读到该订阅")
	}
	var n int
	if err := st.DB.QueryRowContext(ctx, `SELECT count(*) FROM subscriptions WHERE id=$1`, sub.ID).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Fatalf("订阅行仍存在（count=%d），DELETE 必须是物理删除", n)
	}
}

func TestDeleteSubscriptionCascadesNodeGroupLinks(t *testing.T) {
	st := newRegStore(t)
	ctx := context.Background()

	grp := mustCreateNodeGroup(t, st, "del-group")
	sub := mustCreateSubscriptionWithGroups(t, st, "del-sub", []string{grp.ID})

	// 删除前：节点组被订阅引用，删不掉
	if err := st.DeleteNodeGroup(ctx, grp.ID); err == nil {
		t.Fatal("被订阅引用的节点组不应能删除")
	}

	if _, err := st.DeleteSubscription(ctx, sub.ID); err != nil {
		t.Fatalf("DeleteSubscription: %v", err)
	}

	// 关联行应被 ON DELETE CASCADE 清掉
	var links int
	if err := st.DB.QueryRowContext(ctx,
		`SELECT count(*) FROM subscription_node_groups WHERE subscription_id=$1`, sub.ID).Scan(&links); err != nil {
		t.Fatalf("count links: %v", err)
	}
	if links != 0 {
		t.Fatalf("删除订阅后仍残留 %d 条节点组关联", links)
	}

	// 删除后：节点组应可删除（这就是之前"归档订阅永久占引用"导致的问题）
	if err := st.DeleteNodeGroup(ctx, grp.ID); err != nil {
		t.Fatalf("删除订阅后节点组应可删除，实际: %v", err)
	}
}

func TestDeleteSubscriptionRefusesWhenMembersBound(t *testing.T) {
	st := newRegStore(t)
	ctx := context.Background()

	sub := mustCreateSubscription(t, st, "del-bound")
	if err := st.DB.QueryRowContext(ctx, `SELECT 1`).Scan(new(int)); err != nil {
		t.Fatal(err)
	}
	// 直接插一条绑定成员（绕过业务校验，只验证删除时的引用保护）
	if _, err := st.DB.ExecContext(ctx, `INSERT INTO members (id, username, password_hash, subscription_id, role)
		VALUES ('mbr_del_bound','del-bound-user','x',$1,'member')`, sub.ID); err != nil {
		t.Fatalf("insert member: %v", err)
	}

	_, err := st.DeleteSubscription(ctx, sub.ID)
	if err == nil {
		t.Fatal("仍有成员绑定时必须拒绝删除")
	}
	de, ok := err.(*domain.Error)
	if !ok || de.Code != domain.CodeNodeInUse {
		t.Fatalf("expected NODE_IN_USE, got %#v", err)
	}
	if !strings.Contains(de.Message, "member") {
		t.Fatalf("错误信息应说明是成员引用: %q", de.Message)
	}
}

func TestDeleteSubscriptionRemovesArchivedOne(t *testing.T) {
	st := newRegStore(t)
	ctx := context.Background()

	sub := mustCreateSubscription(t, st, "del-archived")
	// 先归档（模拟旧版本软删留下的数据）
	if _, err := st.UpdateSubscription(ctx, sub.ID, 0, func(v *domain.Subscription) error {
		v.Archived = true
		return nil
	}); err != nil {
		t.Fatalf("archive: %v", err)
	}
	// 归档的订阅同样应能被真正删除，否则旧数据永远清不掉
	if _, err := st.DeleteSubscription(ctx, sub.ID); err != nil {
		t.Fatalf("归档订阅应可删除: %v", err)
	}
	var n int
	if err := st.DB.QueryRowContext(ctx, `SELECT count(*) FROM subscriptions WHERE id=$1`, sub.ID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("归档订阅删除后行仍在")
	}
}

// ─── helpers ──────────────────────────────────────────────────────────────

func mustCreateSubscription(t *testing.T, st *Store, name string) *domain.Subscription {
	t.Helper()
	return mustCreateSubscriptionWithGroups(t, st, name, nil)
}

func mustCreateSubscriptionWithGroups(t *testing.T, st *Store, name string, groupIDs []string) *domain.Subscription {
	t.Helper()
	ctx := context.Background()
	id := "sub_" + name
	if _, err := st.DB.ExecContext(ctx, `INSERT INTO subscriptions
		(id, name, description, quota_bytes, timezone, anchor_day, default_valid_days,
		 allow_new_bindings, access_enabled)
		VALUES ($1,$2,'',1073741824,'UTC',1,30,true,true)`, id, name); err != nil {
		t.Fatalf("insert subscription: %v", err)
	}
	for i, gid := range groupIDs {
		if _, err := st.DB.ExecContext(ctx,
			`INSERT INTO subscription_node_groups (subscription_id, node_group_id, position)
			 VALUES ($1,$2,$3)`, id, gid, i); err != nil {
			t.Fatalf("link group: %v", err)
		}
	}
	sub, err := st.GetSubscription(ctx, id)
	if err != nil {
		t.Fatalf("GetSubscription: %v", err)
	}
	return sub
}

func mustCreateNodeGroup(t *testing.T, st *Store, name string) *domain.NodeGroup {
	t.Helper()
	ctx := context.Background()
	id := "ng_" + name
	if _, err := st.DB.ExecContext(ctx,
		`INSERT INTO node_groups (id, name, description) VALUES ($1,$2,'')`, id, name); err != nil {
		t.Fatalf("insert node group: %v", err)
	}
	g, err := st.GetNodeGroup(ctx, id)
	if err != nil {
		t.Fatalf("GetNodeGroup: %v", err)
	}
	return g
}

// 归档成员不该阻止删除订阅。
//
// 成员删除本身是软删（置 archived），归档成员仍持有 subscription_id。
// 如果订阅删除把它们算作引用，两条 DELETE 就互相卡死：成员删不掉（软删）、
// 订阅被归档成员挡住、归档数据永远无法清理。所以删订阅时先解除归档成员的
// 关联，只保护**活跃**成员。
func TestDeleteSubscriptionDetachesArchivedMembers(t *testing.T) {
	st := newRegStore(t)
	ctx := context.Background()

	sub := mustCreateSubscription(t, st, "del-archived-member")
	if _, err := st.DB.ExecContext(ctx, `INSERT INTO members
		(id, username, password_hash, subscription_id, role, archived, active, proxy_enabled)
		VALUES ('mbr_arch_1','archived-user','x',$1,'member',true,false,false)`, sub.ID); err != nil {
		t.Fatalf("insert archived member: %v", err)
	}

	// 有归档成员时仍应能删除订阅
	if _, err := st.DeleteSubscription(ctx, sub.ID); err != nil {
		t.Fatalf("归档成员不应阻止删除订阅: %v", err)
	}

	// 归档成员的 subscription_id 应被解除，但成员记录本身保留（审计/账本需要）
	var subID *string
	var archived bool
	if err := st.DB.QueryRowContext(ctx,
		`SELECT subscription_id, archived FROM members WHERE id='mbr_arch_1'`).Scan(&subID, &archived); err != nil {
		t.Fatalf("归档成员记录应保留: %v", err)
	}
	if subID != nil {
		t.Fatalf("归档成员的 subscription_id 应被置空，仍为 %q", *subID)
	}
	if !archived {
		t.Fatal("成员记录应仍是归档状态")
	}
}

// 活跃成员必须仍然阻止删除（保护：不能悄悄让在用的人失去订阅）。
func TestDeleteSubscriptionStillBlockedByActiveMember(t *testing.T) {
	st := newRegStore(t)
	ctx := context.Background()

	sub := mustCreateSubscription(t, st, "del-active-member")
	if _, err := st.DB.ExecContext(ctx, `INSERT INTO members
		(id, username, password_hash, subscription_id, role, archived, active, proxy_enabled)
		VALUES ('mbr_act_1','active-user','x',$1,'member',false,true,true)`, sub.ID); err != nil {
		t.Fatalf("insert active member: %v", err)
	}
	if _, err := st.DeleteSubscription(ctx, sub.ID); err == nil {
		t.Fatal("活跃成员绑定必须阻止删除订阅")
	}
	var subID *string
	if err := st.DB.QueryRowContext(ctx,
		`SELECT subscription_id FROM members WHERE id='mbr_act_1'`).Scan(&subID); err != nil {
		t.Fatal(err)
	}
	if subID == nil {
		t.Fatal("删除被拒绝时不应改动活跃成员的绑定")
	}
}
