package store

// 归档节点后必须从组里摘除。
//
// 曾经 DeleteNode 只把节点标 archived，node_group_members 里的关联留着，
// 于是组的 node_ids 里出现"幽灵节点"：编辑组时能看见、点开 404，
// 而订阅导出又按 archived 过滤而少一个 —— 前端看到 6 个、订阅只出 5 个。
// （真实环境复现：面板上组显示 6 个节点，labuser 订阅只渲染 5 个。）

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"proxypanel/internal/panel/domain"
)

func TestDeleteNodeRemovesGroupMembership(t *testing.T) {
	st := newRegStore(t)
	ctx := context.Background()

	const serverID = "srv_cleanup"
	if _, err := st.DB.ExecContext(ctx,
		`INSERT INTO servers (id, name, address, region) VALUES ($1,'cleanup-srv','node.example.invalid','test')`,
		serverID); err != nil {
		t.Fatalf("insert server: %v", err)
	}

	node, err := st.CreateNode(ctx, &domain.Node{
		ID: "nd_cleanup", ServerID: serverID, Name: "cleanup-node", Enabled: true,
		Host: "cleanup.example.invalid", Port: 20110, Listen: "0.0.0.0", Protocol: "vless",
	}, "ci_cleanup")
	if err != nil {
		t.Fatalf("create node: %v", err)
	}

	group, err := st.CreateNodeGroup(ctx, &domain.NodeGroup{
		ID: "ng_cleanup", Name: "cleanup-group", NodeIDs: []string{node.ID},
	})
	if err != nil {
		t.Fatalf("create group: %v", err)
	}

	ids, err := st.nodeGroupNodeIDs(ctx, group.ID)
	if err != nil {
		t.Fatalf("group before delete: %v", err)
	}
	if len(ids) != 1 || ids[0] != node.ID {
		t.Fatalf("group before delete = %v, want [%s]", ids, node.ID)
	}

	if _, err := st.DeleteNode(ctx, node.ID); err != nil {
		t.Fatalf("delete node: %v", err)
	}

	// 读取路径不能列出它。
	ids, err = st.nodeGroupNodeIDs(ctx, group.ID)
	if err != nil {
		t.Fatalf("group after delete: %v", err)
	}
	if len(ids) != 0 {
		t.Fatalf("group still lists the archived node: %v", ids)
	}

	// 并且关联行是真的删了，不只是读取时被过滤 —— 否则节点被恢复（archived
	// 置回 false）时会"复活"进组，管理员完全没做过这个操作。
	var rows int
	if err := st.DB.QueryRowContext(ctx,
		`SELECT count(*) FROM node_group_members WHERE node_id=$1`, node.ID).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatalf("node_group_members rows for archived node = %d, want 0", rows)
	}
}

// 软删之后不能再改。
//
// 软删只把行的 archived 置 true，行还在，所以按 id 读写都会命中。以前
// UpdateServer/UpdateNode/UpdateSubscription/UpdateMember 都不看 archived，
// 于是 DELETE 之后的 PATCH 仍然 200、字段真的被改（实测 notes 被写进去了）。
// 并发下这直接表现为 "DELETE 和 PATCH 同时成功"。
//
// 例外是把 archived 改回 false —— 那是"恢复"，必须允许。
func TestArchivedObjectsAreNotUpdatable(t *testing.T) {
	st := newRegStore(t)
	ctx := context.Background()

	const serverID = "srv_archived_guard"
	if _, err := st.DB.ExecContext(ctx,
		`INSERT INTO servers (id, name, address, region) VALUES ($1,'archived-guard','10.0.0.1','test')`,
		serverID); err != nil {
		t.Fatalf("insert server: %v", err)
	}

	// 服务端的 DELETE 就是"归档"：API 层调用 UpdateServer(id, 0, sv.Archived=true)。
	deleted, err := st.UpdateServer(ctx, serverID, 0, func(sv *domain.Server) error {
		sv.Archived = true
		return nil
	})
	if err != nil {
		t.Fatalf("archive server: %v", err)
	}
	if !deleted.Archived {
		t.Fatal("delete did not archive the server")
	}

	// 已归档 → PATCH 必须失败。
	_, err = st.UpdateServer(ctx, serverID, 0, func(sv *domain.Server) error {
		sv.Notes = "改了已删除的服务器"
		return nil
	})
	if err == nil {
		t.Fatal("updating an archived server succeeded; deleted objects must not be editable")
	}
	var domErr *domain.Error
	if !errors.As(err, &domErr) || domErr.Code != domain.CodeNotFound {
		t.Fatalf("update archived server error = %v, want NOT_FOUND", err)
	}
	if domErr.Status != http.StatusNotFound {
		t.Fatalf("not-found status = %d, want 404", domErr.Status)
	}

	// 归档状态和 notes 都不能被改。
	var archived bool
	var notes string
	if err := st.DB.QueryRowContext(ctx,
		`SELECT archived, coalesce(notes,'') FROM servers WHERE id=$1`, serverID).Scan(&archived, &notes); err != nil {
		t.Fatal(err)
	}
	if !archived {
		t.Fatal("archived flag was cleared by the rejected update")
	}
	if notes != "" {
		t.Fatalf("notes = %q, want empty: the rejected update still wrote a field", notes)
	}

	// 恢复（archived → false）必须允许，否则删错了就救不回来。
	restored, err := st.UpdateServer(ctx, serverID, 0, func(sv *domain.Server) error {
		sv.Archived = false
		sv.Notes = "restored"
		return nil
	})
	if err != nil {
		t.Fatalf("restoring an archived server must be allowed: %v", err)
	}
	if restored.Archived || restored.Notes != "restored" {
		t.Fatalf("restore result = archived:%v notes:%q", restored.Archived, restored.Notes)
	}
}
