-- readiness: schema-only
--
-- 只给 nodes 加三个 jsonb 列，默认空对象/空数组，不读写任何计量数据：
--   usage_entries / usage_batches / batches / batch_items / members / tasks 全都不碰。
-- 因此升级预检不需要外部 Agent 停止态证据（见 store/pending_scope.go）。
-- 只要这份迁移以后开始改写已有行或触碰计量表，就必须删掉上面那行声明。
-- 入站级字段：sniffing / allocate / external_proxy
--
-- 之前面板只持久化三个对象 —— stream_settings（streamSettings）、
-- protocol_settings（settings 里除 clients 的部分）、client_settings（成员客户端参数）。
-- 但 Xray 的入站还有三个平级对象不属于它们：sniffing（域名嗅探）、allocate（UDP 端口
-- 分配）、externalProxy（对外暴露的代理地址）。面板里三个 JSON 编辑器写不进去，
-- 因为持久化层就没有位置：sniffing 曾经是写死的（enabled=true / [http,tls,quic]），
-- allocate 与 externalProxy 从未生成。
--
-- 这里按 Xray / 3x-ui 的形状各给一列，不合并成一个自定义袋子：
--   sniffing   object  {enabled, destOverride[], metadataOnly, routeOnly}
--   allocate   object  {strategy, refresh, concurrency}
--   external_proxy array [{forceTls, dest, port, remark}]
--
-- 默认值与当前实际行为一致（sniffing 默认开启且 destOverride 为 http/tls/quic），
-- 所以旧库升级后生成的配置不变。列内未知键原样保留，与 stream_settings 的做法一致。
ALTER TABLE nodes ADD COLUMN IF NOT EXISTS sniffing jsonb NOT NULL DEFAULT '{}'::jsonb;
ALTER TABLE nodes ADD COLUMN IF NOT EXISTS allocate jsonb NOT NULL DEFAULT '{}'::jsonb;
ALTER TABLE nodes ADD COLUMN IF NOT EXISTS external_proxy jsonb NOT NULL DEFAULT '[]'::jsonb;

COMMENT ON COLUMN nodes.sniffing IS 'Xray inbound sniffing 对象；未知键原样保留';
COMMENT ON COLUMN nodes.allocate IS 'Xray inbound allocate 对象；未知键原样保留';
COMMENT ON COLUMN nodes.external_proxy IS 'Xray inbound externalProxy 数组；未知键原样保留';
