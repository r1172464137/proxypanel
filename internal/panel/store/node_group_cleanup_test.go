package store

// 归档节点后必须从组里摘除。
//
// 曾经 DeleteNode 只把节点标 archived，node_group_members 里的关联留着，
// 于是组的 node_ids 里出现"幽灵节点"：编辑组时能看见、点开 404，
// 而订阅导出又按 archived 过滤而少一个 —— 前端看到 6 个、订阅只出 5 个。
// （真实环境复现：面板上组显示 6 个节点，labuser 订阅只渲染 5 个。）

import (
	"context"
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
