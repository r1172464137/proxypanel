-- 节点的「对外端口」：让客户端连接的端口与实际监听端口分离。
--
-- 背景：v2board 那类面板支持"节点实际跑在 A 机的 20101，但对外暴露 B 机的 443"。
-- 本平台已有 host（客户端看到的地址）与 listen（监听地址）分离，但两者共用
-- 同一个 port，所以做不了「对外 443 → 实际 20101」。
--
-- 这里只补地址层面的分离：external_port 为空时沿用 port（完全向后兼容）；
-- 填了之后客户端配置用 host:external_port，而服务端仍监听 port。
-- 端口转发（B 机 443 → A 机 20101）由运维在机器上自行配置，面板不代管 ——
-- 这样不需要改 Agent 协议，也不会把转发状态变成面板的隐藏状态。
ALTER TABLE nodes ADD COLUMN IF NOT EXISTS external_port integer;

-- 与 port 用同一取值范围；NULL 表示"与 port 相同"。
ALTER TABLE nodes DROP CONSTRAINT IF EXISTS nodes_external_port_check;
ALTER TABLE nodes ADD CONSTRAINT nodes_external_port_check
    CHECK (external_port IS NULL OR external_port BETWEEN 1 AND 65535);
