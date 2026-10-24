/** Real Chromium + owned Panel + isolated PostgreSQL. Run product-readiness-env.sh.
 * No route interception, screenshots, traces, HAR, or external/lab processes.
 * SQL fixtures below are explicitly synthetic persisted state, NOT Agent reports.
 */
import { chromium, expect } from '@playwright/test'
import { readFile, writeFile, mkdir, copyFile } from 'node:fs/promises'
import { spawn, execFileSync } from 'node:child_process'
import { createServer } from 'node:net'
import { join, resolve } from 'node:path'
import assert from 'node:assert/strict'

const privateDir = process.env.PR_PRIVATE
if (!privateDir?.startsWith('/tmp/pr-browser.')) throw Error('Run owned environment launcher')
const root = resolve(import.meta.dirname, '../..')
const owner = JSON.parse(await readFile(join(privateDir, 'owner.json'), 'utf8'))
const run = new Date().toISOString().replace(/[:.]/g, '-')
const out = join(process.env.PR_MAIN_ROOT, '.ai/evidence/browser/product-readiness', run)
await mkdir(out, { recursive: true, mode: 0o700 })
await writeFile(join(privateDir, 'evidence-path'), out, { mode: 0o600 })
const secrets = [owner.PANEL_MASTER_KEY, owner.PANEL_ADMIN_PASSWORD]
const redact = s => String(s).replace(/\/sub\/[^\s"'?]+/g, '/sub/[REDACTED]').replace(/pr_browser_[a-f0-9]{32}/g, '[owned-schema]').split('\n').map(l => secrets.reduce((v, secret) => secret ? v.split(secret).join('[REDACTED]') : v, l)).join('\n')
const results = [], requests = [], fixtures = [], pageErrors = [], observations = []
let panel, browser, browserVersion, base, admin, member, ap, mp, server, node, group, subscription, invitation, memberId, batchPath
process.on('SIGTERM', () => { panel?.kill('SIGTERM'); void browser?.close() })
process.on('SIGINT', () => { panel?.kill('SIGTERM'); void browser?.close() })
const sql = statement => JSON.parse(execFileSync(join(privateDir, 'db'), ['sql', join(privateDir, 'owner.json')], { input: statement, encoding: 'utf8' }))
const q = value => `'${String(value).replaceAll("'", "''")}'`
async function test(name, fn) {
 const started = new Date().toISOString()
 try { const detail = await fn(); results.push({ name, started, status: 'PASS', detail }); console.log(`PASS ${name}`); return true }
 catch (e) { results.push({ name, started, status: 'FAIL', error: redact(e.stack) }); console.log(`FAIL ${name}: ${redact(e.message).slice(0, 240)}`); return false }
 finally { await writeFile(join(out, 'results.json'), redact(JSON.stringify(results, null, 2)), { mode: 0o600 }) }
}
async function api(context, path, method = 'GET', data, expected = 200) {
 const session = method === 'GET' || path.startsWith('/auth/') ? null : await context.request.get(base + '/api/v1/auth/session').then(r => r.json())
 const csrf = session?.data?.csrf_token
 const response = await context.request.fetch(base + '/api/v1' + path, { method, data, headers: { ...(csrf ? { 'X-CSRF-Token': csrf } : {}) } })
 const payload = await response.json()
 requests.push({ at: new Date().toISOString(), method, path: redact('/api/v1' + path), status: response.status(), requestId: response.headers()['x-request-id'], source: 'real API setup/assertion' })
 assert.equal(response.status(), expected, `${method} ${path}: HTTP ${response.status()} ${payload.error?.code ?? ''}`)
 return payload.data ?? payload
}
async function context() {
 const c = await browser.newContext({ timezoneId: 'UTC', permissions: ['clipboard-read', 'clipboard-write'] })
 c.setDefaultTimeout(12000)
 c.on('page', p => {
  p.on('pageerror', e => pageErrors.push(redact(e.message)))
  p.on('response', r => { const u = new URL(r.url()); if (u.pathname.startsWith('/api/') || u.pathname.startsWith('/sub/')) requests.push({ at: new Date().toISOString(), method: r.request().method(), path: redact(u.pathname), status: r.status(), requestId: r.headers()['x-request-id'] }) })
 })
 return c
}
async function login(c, username) {
 const p = await c.newPage(); await p.goto(base + '/login')
 await p.getByLabel('用户名', { exact: true }).fill(username)
 await p.getByLabel('密码', { exact: true }).fill(owner.PANEL_ADMIN_PASSWORD)
 await p.getByRole('button', { name: '登录', exact: true }).click()
 await p.waitForURL(username === 'admin' ? '**/admin/overview' : '**/me/subscription')
 return p
}
async function makeInvite(sub) {
 await ap.goto(base + '/admin/invitations'); await ap.getByRole('button', { name: '生成邀请码', exact: true }).first().click()
 await ap.locator('#invite-sub').selectOption(sub || '')
 const response = ap.waitForResponse(r => r.url().endsWith('/admin/invitations') && r.request().method() === 'POST')
 await ap.getByRole('button', { name: '生成', exact: true }).click()
 const r = await response; assert.equal(r.status(), 201); const v = (await r.json()).data; secrets.push(v.code)
 await expect(ap.getByText('邀请码只展示一次，关闭后列表不会再次显示 code。')).toBeVisible()
 await ap.getByRole('button', { name: '关闭', exact: true }).last().click()
 return v
}
async function redeem(code, name, success = true) {
 const c = await context(), p = await c.newPage(); await p.goto(base + '/redeem')
 await p.locator('#redeem-code').fill(code); await p.locator('#redeem-username').fill(name)
 await p.locator('#redeem-password').fill(owner.PANEL_ADMIN_PASSWORD); await p.locator('#redeem-confirm').fill(owner.PANEL_ADMIN_PASSWORD)
 const pending = p.waitForResponse(r => r.url().endsWith('/auth/redeem') && r.request().method() === 'POST')
 await p.getByRole('button', { name: '兑换并登录', exact: true }).click()
 const r = await pending
 if (success) assert.equal(r.status(), 201); else { assert.ok(r.status() >= 400 && r.status() < 500); await expect(p.locator('[role="alert"]').first()).toBeVisible() }
 await c.close(); return r.status()
}
async function subInfo() { const s = await api(member, '/me/subscription'); secrets.push(s.token); return s }
function subscriptionUrl(s) { return s.url.replace('{token}', encodeURIComponent(s.token)) }
function credentialIds() { return sql(`select id,state from credentials where member_id=${q(memberId)} order by id`) }

try {
 const port = await new Promise((resolvePort, reject) => { const s = createServer(); s.once('error', reject); s.listen(0, '127.0.0.1', () => { const port = s.address().port; s.close(() => resolvePort(port)) }) })
 base = `http://127.0.0.1:${port}`
 const log = await import('node:fs').then(fs => fs.openSync(join(privateDir, 'panel.log'), 'a', 0o600))
 panel = spawn(join(privateDir, 'panel'), [], { cwd: root, env: { PATH: process.env.PATH, DATABASE_URL: owner.DATABASE_URL, PANEL_MASTER_KEY: owner.PANEL_MASTER_KEY, PANEL_ADMIN_USER: 'admin', PANEL_ADMIN_PASSWORD: owner.PANEL_ADMIN_PASSWORD, PANEL_DEV: 'true', PANEL_LISTEN: `127.0.0.1:${port}`, PANEL_PUBLIC_URL: base, PANEL_WEB_ROOT: join(privateDir, 'dist') }, stdio: ['ignore', log, log] })
 await test('owned Panel startup and exclusive schema', async () => {
  for (let i = 0; i < 100; i++) { if (panel.exitCode !== null) throw Error('owned Panel exited; inspect sanitized panel.log'); try { const r = await fetch(base + '/readyz'); if (r.ok) break } catch {} await new Promise(r => setTimeout(r, 200)) }
  assert.equal((await fetch(base + '/readyz')).status, 200)
  assert.equal(sql('select current_schema() as schema')[0].schema, owner.schema)
  return { schemaProved: true, publicExcluded: true, panelPid: panel.pid, base, transport: 'loopback development HTTP; NOT HTTPS acceptance' }
 })
 browser = await chromium.launch({ headless: true }); browserVersion = browser.version(); expect.configure({ timeout: 10000 })
 admin = await context()
 if (!await test('administrator browser login and reload', async () => { ap = await login(admin, 'admin'); await ap.reload(); await expect(ap.getByRole('heading', { level: 1 })).toBeVisible(); return { refreshed: true } })) throw Error('administrator prerequisite failed')
 if (!await test('synthetic resource fixture via real API (no Agent)', async () => {
  server = await api(admin, '/admin/servers', 'POST', { name: 'PR fixture server (NO AGENT)', address: '127.0.0.1' }, 201)
  node = await api(admin, '/admin/nodes', 'POST', { name: 'PR synthetic VLESS', server_id: server.id, host: 'fixture.example.invalid', port: 23456, listen: '127.0.0.1', protocol: 'vless', enabled: true, stream_settings: { network: 'tcp', security: 'none' }, protocol_settings: { decryption: 'none' }, client_settings: {}, sort_order: 0 }, 201)
  group = await api(admin, '/admin/node-groups', 'POST', { name: 'PR synthetic group', node_ids: [node.id] }, 201)
  fixtures.push({ source: 'real admin HTTP API; intentionally not connected to Agent', serverId: server.id, nodeId: node.id, groupId: group.id })
  return fixtures.at(-1)
 })) throw Error('resource prerequisite failed')
 if (!await test('service plan created in browser and persisted after refresh', async () => {
  await ap.goto(base + '/admin/subscriptions/new'); await ap.locator('#sub-name').fill('PR browser 1GiB')
  await ap.getByText('PR synthetic group', { exact: false }).click(); await ap.locator('#sub-quota').fill('1')
  await ap.locator('#sub-tz').fill('UTC'); await ap.locator('#sub-anchor').fill('1'); await ap.getByRole('button', { name: '保存服务方案', exact: true }).click()
  await ap.waitForURL('**/admin/subscriptions'); await ap.reload(); await expect(ap.getByRole('link', { name: 'PR browser 1GiB', exact: true })).toBeVisible()
  subscription = (await api(admin, '/admin/subscriptions')).items.find(s => s.name === 'PR browser 1GiB')
  assert.equal(subscription.quota_bytes, 1073741824); assert.deepEqual(subscription.node_group_ids, [group.id]); return { id: subscription.id, quota: subscription.quota_bytes }
 })) throw Error('plan prerequisite failed')
 await test('invitation browser creation and one-step service registration', async () => {
  invitation = await makeInvite(subscription.id); await redeem(invitation.code, 'pr_member')
  const users = (await api(admin, '/admin/members')).items; const m = users.find(x => x.username === 'pr_member'); assert.equal(m.subscription_id, subscription.id); memberId = m.id
  await ap.reload(); await expect(ap.locator('tr').filter({ hasText: invitation.id })).toContainText('1 / 1'); return { invitationId: invitation.id, memberId, subscriptionId: m.subscription_id }
 })
 member = await context()
 if (!await test('member browser login, persisted service status and zero usage', async () => {
  mp = await login(member, 'pr_member'); await mp.reload(); await expect(mp.getByTestId('service-summary-card')).toContainText('1.00 GiB')
  const s = await api(member, '/me/status'); assert.equal(s.access.access, true); assert.equal(s.access.used_bytes, 0)
  observations.push({ name: 'initial member status', access: s.access.access, accessReasons: s.access.access_reasons, usedBytes: s.access.used_bytes })
  await expect(mp.getByTestId('service-summary-card').locator('dd').first()).toHaveText('0 B'); return { access: true, usedBytes: 0, totalBytes: 1073741824 }
 })) throw Error('member prerequisite failed')
 await test('available service must not simultaneously claim unavailable', async () => {
  await expect(mp.getByTestId('service-summary-card')).not.toContainText('服务暂不可用')
  return { consistentStatus: true }
 })
 await test('client config retrieval/download and copy does not reset persisted secrets', async () => {
  const before = await subInfo(), ids = credentialIds(); const response = mp.waitForResponse(r => new URL(r.url()).pathname.startsWith('/sub/'))
  await mp.getByTestId('import-check-button').click(); assert.equal((await response).status(), 200)
  await expect(mp.getByTestId('import-result-message')).toContainText('尚未验证真实连接')
  const download = mp.waitForEvent('download'); await mp.getByTestId('import-download-button').click(); const d = await download; assert.ok(d.suggestedFilename()); await d.delete()
  await mp.getByRole('button', { name: '复制', exact: true }).first().click()
  assert.equal((await subInfo()).token, before.token); assert.deepEqual(credentialIds(), ids)
  return { configRetrieved: true, downloaded: true, tokenUnchanged: true, credentialsUnchanged: true, realConnection: 'NOT RUN' }
 })
 await test('subscription reset only invalidates old URL; connection credentials unchanged', async () => {
  const before = await subInfo(), ids = credentialIds(); const oldURL = subscriptionUrl(before)
  await mp.goto(base + '/me/credentials'); await mp.getByRole('button', { name: '重置订阅链接', exact: true }).click()
  await expect(mp.getByRole('alertdialog')).toContainText('代理凭据不变')
  await mp.getByRole('button', { name: '重置链接', exact: true }).click()
  await expect.poll(async () => (await subInfo()).token !== before.token).toBe(true)
  const after = await subInfo(); const old = await member.request.get(new URL(oldURL, base).href); assert.ok([401, 403, 404, 410].includes(old.status()))
  assert.equal((await member.request.get(new URL(subscriptionUrl(after), base).href)).status(), 200); assert.deepEqual(credentialIds(), ids)
  await mp.reload(); assert.equal((await subInfo()).token, after.token); return { oldURLRejected: old.status(), newURL: 200, connectionCredentialsUnchanged: true }
 })
 await test('connection reset changes persisted credentials, not token; UI explains asynchronous effect', async () => {
  const before = await subInfo(), ids = credentialIds(); await mp.goto(base + '/me/credentials')
  await mp.getByRole('button', { name: '重置连接凭据', exact: true }).click(); await mp.locator('#cred-node').selectOption(node.id)
  await mp.getByRole('alertdialog').getByRole('button', { name: '重置连接凭据', exact: true }).click()
  await expect(mp.getByRole('alertdialog')).toContainText('生效以节点回执为准')
  assert.notDeepEqual(credentialIds(), ids); assert.equal((await subInfo()).token, before.token)
  await mp.reload(); const after = credentialIds(); assert.equal(after.filter(c => c.state === 'active').length, 1)
  assert.ok(after.some(c => c.state === 'revoked')); assert.notEqual(after.find(c => c.state === 'active').id, ids.find(c => c.state === 'active').id)
  const tasks = sql(`select state from tasks order by created_at desc limit 5`)
  return { credentialIdsChangedAndPersisted: true, oldCredentialRevoked: true, subscriptionTokenUnchanged: true, taskStates: tasks, noAgentRegistered: true, agentEffect: 'NOT RUN; no dispatch task expected without an Agent' }
 })
 await test('member status and usage pages use persisted ledger', async () => {
  for (const path of ['/me/status', '/me/usage']) { await mp.goto(base + path); await mp.reload(); await expect(mp.getByRole('heading', { level: 1 })).toBeVisible(); await expect(mp.locator('main .banner--danger[role="alert"]')).toHaveCount(0) }
  return { source: 'real empty ledger, no synthetic traffic asserted' }
 })
 await test('synthetic 3MiB ledger fixture agrees with member status and browser usage', async () => {
  const statement = `insert into usage_entries(agent_id,batch_id,member_id,node_id,credential_id,period_start,period_end,up_bytes,down_bytes,sample_started_at,sample_ended_at,core_generation) select 'pr_SYNTHETIC_agent','pr_SYNTHETIC_3MiB',id,${q(node.id)},'',period_start,period_end,1048576,2097152,now()-interval '2 minutes',now()-interval '1 minute','SYNTHETIC_NOT_AGENT' from members where id=${q(memberId)} returning member_id,up_bytes,down_bytes`
  const rows = sql(statement); assert.equal(rows.length, 1); fixtures.push({ source: 'direct SQL synthetic ledger; NOT real Agent traffic', rows })
  const s = await api(member, '/me/status'); assert.equal(s.access.used_bytes, 3145728)
  await mp.goto(base + '/me/subscription'); await expect(mp.getByTestId('service-summary-card').locator('dd').first()).toHaveText('3.00 MiB')
  await mp.goto(base + '/me/usage'); await mp.reload(); await expect(mp.locator('body')).toContainText('1.00 MiB'); await expect(mp.locator('body')).toContainText('2.00 MiB')
  return { synthetic: true, persistedUpBytes: 1048576, persistedDownBytes: 2097152, statusUsedBytes: 3145728, noAgentTrafficClaim: true }
 })
 await test('no-plan invitation gives login and actionable unavailable state', async () => {
  const invite = await makeInvite(null); await redeem(invite.code, 'pr_no_plan'); const c = await context(), p = await login(c, 'pr_no_plan')
  await expect(p.getByTestId('service-summary-card')).toContainText(/未.*方案|未.*订阅|待开通|尚未开通/)
  const s = await api(c, '/me/status'); assert.equal(s.access.access, false); await expect(p.getByTestId('client-import-card')).toContainText('联系为你开通服务的管理员'); await c.close()
  return { access: false, importUnavailable: true }
 })
 await test('used invitation cannot register another member (browser plus persisted count)', async () => {
  const status = await redeem(invitation.code, 'pr_reuse_denied', false)
  assert.equal(sql("select count(*)::int as count from members where username='pr_reuse_denied'")[0].count, 0)
  return { rejectedStatus: status, createdAccounts: 0 }
 })
 await test('member role rejects admin API and admin browser route', async () => {
  const r = await member.request.get(base + '/api/v1/admin/members'); assert.equal(r.status(), 403)
  await mp.goto(base + '/admin/members'); await mp.waitForURL('**/me/subscription?denied=**'); await expect(mp.getByRole('button', { name: '新建成员', exact: true })).toHaveCount(0)
  assert.ok(new URL(mp.url()).searchParams.get('denied') === '/admin/members'); return { api: 403, browserManagementDenied: true }
 })
 await test('illegal cursors fail closed across real list endpoints', async () => {
  const statuses = {}; for (const name of ['members','nodes','subscriptions','invitations','tasks','events','audit-logs']) { const r = await admin.request.get(base + `/api/v1/admin/${name}?limit=1&cursor=definitely-invalid`); statuses[name] = r.status(); assert.equal(r.status(), 400, name) } return statuses
 })
 await test('bulk UI preview submit durable record and refreshed member state', async () => {
  await ap.goto(base + '/admin/members'); const row = ap.locator('tr').filter({ has: ap.getByRole('link', { name: 'pr_member', exact: true }) })
  await row.getByRole('checkbox').check(); await ap.getByRole('button', { name: /批量操作（已选/ }).click()
  await ap.locator('#batch-op').selectOption('set_expiry'); await ap.locator('#batch-expiry').fill('2031-01-02T03:04')
  await ap.getByRole('button', { name: '生成预览', exact: true }).click()
  await expect(ap.getByRole('dialog')).toContainText('预览结果（未提交）')
  await ap.getByRole('dialog').getByRole('checkbox').check(); await ap.getByRole('button', { name: '确认提交', exact: true }).click()
  const record = ap.getByRole('link', { name: '查看本次变更记录', exact: true }); await expect(record).toBeVisible(); batchPath = await record.getAttribute('href'); await record.click()
  await ap.reload(); await expect(ap.getByRole('heading', { level: 1 })).toBeVisible()
  const detail = await api(admin, batchPath.replace('/admin', '/admin')); assert.ok(detail)
  const m = await api(admin, `/admin/members/${memberId}`); assert.equal((m.member ?? m).expires_at, '2031-01-02T03:04:00Z')
  return { batchPath, persistedExpiry: (m.member ?? m).expires_at }
 })
 await test('PR018 more-than-one-page search and cross-page selection scope', async () => {
  for (let i = 0; i < 51; i++) await api(admin, '/admin/members', 'POST', { username: `pr_page_${String(i).padStart(3, '0')}`, password: owner.PANEL_ADMIN_PASSWORD, role: 'member', active: true, proxy_enabled: false }, 201)
  fixtures.push({ source: '51 real API-created synthetic accounts', names: 'pr_page_000 through pr_page_050' })
  await ap.goto(base + '/admin/members'); await ap.getByPlaceholder('按用户名或 ID 过滤当前页').fill('pr_page_'); await ap.getByRole('button', { name: '搜索全部成员', exact: true }).click()
  await expect(ap.locator('tbody tr')).toHaveCount(50)
  const first = await ap.locator('tbody tr a').allTextContents(); await ap.locator('tbody tr').first().getByRole('checkbox').check()
  await ap.getByRole('button', { name: '下一页', exact: true }).click(); await expect(ap.locator('tbody tr')).toHaveCount(1)
  const second = await ap.locator('tbody tr a').allTextContents(); assert.equal(new Set([...first, ...second].filter(x => x.startsWith('pr_page_'))).size, 51)
  await ap.locator('tbody tr').first().getByRole('checkbox').check(); await expect(ap.getByRole('button', { name: /批量操作（已选 2/ })).toBeEnabled()
  await ap.getByPlaceholder('按用户名或 ID 过滤当前页').fill('different-query-not-submitted')
  await expect(ap.getByRole('button', { name: /批量操作（已选 2/ })).toBeDisabled()
  return { matchingAccounts: 51, firstPage: 50, secondPage: 1, selection: 2, dirtyQuerySubmitBlocked: true }
 })
 await test('real no-Agent runtime is unknown, not zero/healthy', async () => {
  await ap.goto(base + `/admin/servers/${server.id}/runtime`); await ap.reload()
  await expect(ap.locator('body')).toContainText('未知'); await expect(ap.locator('body')).toContainText('该服务器未注册 Agent')
  return { source: 'real server without an Agent', unknownVisible: true, telemetry: 'not zero or connected' }
 })
 await test('unlimited differs from zero on real persisted plan', async () => {
  const unlimited = await api(admin, '/admin/subscriptions', 'POST', { name: 'PR unlimited', node_group_ids: [group.id], quota_bytes: null, timezone: 'UTC', anchor_day: 1, allow_new_bindings: true, access_enabled: true }, 201)
  const invite = await makeInvite(unlimited.id); await redeem(invite.code, 'pr_unlimited'); const c = await context(), p = await login(c, 'pr_unlimited')
  await expect(p.getByTestId('service-summary-card')).toContainText('不限量'); assert.equal((await api(c, '/me/status')).access.effective_quota_bytes, null); await c.close(); return { total: null, label: '不限量', distinctFromZero: true }
 })
 await test('synthetic rollback/failed target fixture actually visible (NOT Agent evidence)', async () => {
  const statement = `update core_instances set generation=1,config_revision=1,state='rollback_applied',last_error='SYNTHETIC fixture: target rejected, previous config running' where node_id=${q(node.id)} returning id,state,last_error`
  const rows = sql(statement); fixtures.push({ source: 'direct SQL synthetic runtime fixture; no Agent or actual rollback', rows })
  await ap.goto(base + `/admin/servers/${server.id}/runtime`); await ap.reload()
  await expect(ap.locator('body')).toContainText(/回滚|上一份|旧配置/); await expect(ap.locator('body')).toContainText('SYNTHETIC fixture: target rejected')
  return { visible: true, source: 'persisted synthetic core instance only' }
 })
 await test('maintenance blocks normal writes and persists unverified state', async () => {
  await api(admin, '/admin/recovery-mode/enter', 'POST', {})
  assert.equal((await api(admin, '/admin/recovery-mode')).recovery_mode, true)
  await api(member, '/me/subscription/token/rotations', 'POST', {}, 409)
  await api(admin, '/admin/invitations', 'POST', {}, 409)
  await api(admin, '/admin/recovery-mode/exit', 'POST', {}, 400)
  assert.equal((await api(admin, '/admin/recovery-mode')).recovery_mode, true)
  return { blockedWrites: true, unverifiedExitBlocked: true, maintenancePersisted: true }
 })
 await test('maintenance is actually visible to existing member, not success-shaped import', async () => {
  await mp.goto(base + '/me/subscription'); await mp.reload()
  await expect(mp.getByTestId('service-summary-card')).toBeVisible()
  observations.push({ name: 'existing-member maintenance visibility', subscriptionHTTP: (await member.request.get(base + '/api/v1/me/subscription')).status(), downloadVisible: await mp.getByTestId('import-download-button').isVisible() })
  await expect(mp.locator('body')).toContainText('恢复核对进行中')
  return { visible: true }
 })
 await test('no unhandled browser runtime errors', async () => { assert.deepEqual(pageErrors, []); return { pageErrors: 0 } })
} catch (e) {
 results.push({ name: 'prerequisite/environment', status: 'FAIL', error: redact(e.stack) })
} finally {
 if (browser) await browser.close()
 if (panel && panel.exitCode === null) { panel.kill('SIGTERM'); await Promise.race([new Promise(r => panel.once('exit', r)), new Promise(r => setTimeout(r, 12000))]); if (panel.exitCode === null) { panel.kill('SIGKILL'); await new Promise(r => panel.once('exit', r)) } }
 const log = await readFile(join(privateDir, 'panel.log'), 'utf8').catch(() => '')
 await writeFile(join(out, 'panel.log'), redact(log), { mode: 0o600 })
 await copyFile(join(privateDir, 'schema-proof.json'), join(out, 'schema-proof.json'))
 await copyFile(join(privateDir, 'build.log'), join(out, 'build.log'))
 const metadata = { candidate: execFileSync('git', ['rev-parse', 'HEAD'], { cwd: root, encoding: 'utf8' }).trim(), run, playwright: JSON.parse(await readFile(join(root, 'web/node_modules/playwright/package.json'), 'utf8')).version, chromium: { executable: chromium.executablePath(), version: browserVersion }, scope: 'real loopback HTTP Panel-hosted web and real PostgreSQL only', notRun: ['HTTPS', 'independent static hosting / dual origin', 'Agent registration and data plane', 'actual traffic and usage reporting', 'actual rollback', 'unknown telemetry state (no API mocking)'], fixtures, observations, results, requests }
 await writeFile(join(out, 'report.json'), redact(JSON.stringify(metadata, null, 2)), { mode: 0o600 })
 await writeFile(join(out, 'results.json'), redact(JSON.stringify(results, null, 2)), { mode: 0o600 })
 console.log(`Evidence: ${out}`)
 console.log(`${results.filter(x => x.status === 'PASS').length} PASS / ${results.filter(x => x.status === 'FAIL').length} FAIL; unrun scopes explicitly listed`)
}
process.exitCode = results.some(x => x.status === 'FAIL') ? 1 : 0
