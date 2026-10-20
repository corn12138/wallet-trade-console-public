/**
 * Campaign push cancellation e2e driver, run by campaign-push-cancel-e2e.py.
 * Drives the real cmd/api process (Socket.IO /campaign-events, REST, PostgreSQL)
 * with the repository's socket.io-client: an unsubscribe while a subscribe is
 * blocked in its durable read, a replacement subscribe before the old one
 * completes, repeated unsubscribe, and disconnect during an observation.
 * Sessions are server-issued test sessions, not wallet or browser evidence.
 * Checks named "fixture: ..." are setup; every other failing check is a
 * product invariant. Credentials come from a config file and are redacted.
 */
'use strict';

const fs = require('fs');
const path = require('path');
const crypto = require('crypto');
const { spawn, execFileSync } = require('child_process');

const config = JSON.parse(fs.readFileSync(process.env.PUSH_E2E_CONFIG, 'utf8'));
const { io } = require(path.join(process.env.WEB_NM, 'socket.io-client'));
const BASE = `http://127.0.0.1:${config.port}`;
const secrets = [config.tokens.a, config.tokens.b, config.tokens.expired, config.tokens.oldEpoch, config.tokens.revoked, config.apiEnv.WEB3_JWT_SECRET, ...(config.redact || [])].filter(Boolean);
const results = [];
const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
const redact = (text) => secrets.reduce((value, secret) => value.split(secret).join('<redacted>'), String(text));

function check(name, pass, detail = {}) {
  results.push({ name, pass: Boolean(pass), detail: JSON.parse(redact(JSON.stringify(detail))) });
  console.log(`${pass ? 'PASS' : 'FAIL'}  ${name}`);
}

// ---------- process control ----------
let api = null;
const apiLog = fs.openSync(config.apiLog, 'a');
function startApi(extra = {}) {
  const env = { PATH: process.env.PATH, HOME: process.env.HOME, ...config.apiEnv, API_GO_LISTEN_ADDR: `127.0.0.1:${config.port}`, ...extra };
  fs.writeSync(apiLog, `\n==== start ${new Date().toISOString()} ${JSON.stringify(Object.keys(extra))}\n`);
  const child = spawn(config.apiBinary, [], { env, stdio: ['ignore', apiLog, apiLog] });
  const exited = new Promise((resolve) => child.on('exit', (code, signal) => resolve({ code, signal })));
  api = { child, exited };
  fs.appendFileSync(config.pidFile, `${child.pid}\n`);
  return waitHealthy();
}
async function waitHealthy() {
  for (let i = 0; i < 200; i++) {
    try { if ((await fetch(`${BASE}/healthz`)).ok) return; } catch { /* starting */ }
    await sleep(100);
  }
  throw new Error('API did not become healthy');
}
async function stopApi(signal = 'SIGTERM') {
  if (!api) return null;
  const current = api;
  api = null;
  current.child.kill(signal);
  const timeout = sleep(15000).then(() => ({ code: null, signal: 'timeout' }));
  return Promise.race([current.exited, timeout]);
}

function psql(sql) {
  return execFileSync(config.psql, ['-X', config.databaseUrl, '-v', 'ON_ERROR_STOP=1', '-Atc', sql], { encoding: 'utf8' }).trim();
}
function fixture(...args) {
  execFileSync(config.fixtureBinary, args, { env: { PATH: process.env.PATH, DATABASE_URL: config.databaseUrl }, encoding: 'utf8' });
}

// ---------- REST ----------
function uuid7() {
  const bytes = crypto.randomBytes(16);
  const now = BigInt(Date.now());
  for (let i = 0; i < 6; i++) bytes[i] = Number((now >> BigInt(8 * (5 - i))) & 0xffn);
  bytes[6] = (bytes[6] & 0x0f) | 0x70;
  bytes[8] = (bytes[8] & 0x3f) | 0x80;
  const hex = bytes.toString('hex');
  return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`;
}
async function rest(method, route, token, body) {
  const headers = { Accept: 'application/json' };
  if (token) headers.Authorization = `Bearer ${token}`;
  if (body) headers['Content-Type'] = 'application/json';
  const response = await fetch(`${BASE}${route}`, { method, headers, body: body ? JSON.stringify(body) : undefined });
  const text = await response.text();
  let json = null;
  try { json = JSON.parse(text); } catch { /* non-JSON */ }
  return { status: response.status, json };
}
const v2 = (route) => `/api/v2${route}`;
async function join(token, id, state) {
  return rest('POST', v2(`/campaigns/${id}/join`), token, { clientCommandId: uuid7(), expectedStateVersion: state, expectedPublicationVersion: config.publicationVersion });
}
async function leave(token, id, state) {
  return rest('DELETE', v2(`/campaigns/${id}/join`), token, { clientCommandId: uuid7(), expectedStateVersion: state });
}

// Anchor-before-REST catch-up, exactly the order createCampaignRecovery uses.
async function catchup(family, token) {
  const id = config.campaignId;
  const feedRoute = family === 'personal' ? `/campaigns/${id}/me/events` : `/campaigns/${id}/events?family=${family}`;
  const sep = feedRoute.includes('?') ? '&' : '?';
  const anchor = await rest('GET', v2(feedRoute), token);
  const snapshot = family === 'personal' ? await rest('GET', v2(`/campaigns/me?ids=${id}`), token) : await rest('GET', v2(`/campaigns/${id}`));
  const after = await rest('GET', v2(`${feedRoute}${sep}cursor=${anchor.json?.cursor}&limit=50`), token);
  let repaired = snapshot;
  if (after.json?.events?.length) {
    repaired = family === 'personal' ? await rest('GET', v2(`/campaigns/me?ids=${id}`), token) : await rest('GET', v2(`/campaigns/${id}`));
  }
  return { anchor: anchor.json, snapshot: repaired.json, after: after.json, statuses: [anchor.status, snapshot.status, after.status] };
}
const participation = (me) => me?.items?.[0]?.participation?.status ?? null;

// ---------- sockets ----------
function client(auth, label) {
  const socket = io(`${BASE}/campaign-events`, {
    path: '/socket.io', transports: ['websocket', 'polling'], forceNew: true, ...(auth !== undefined ? { auth } : {}),
    reconnection: true, reconnectionAttempts: 8, reconnectionDelay: 300, reconnectionDelayMax: 2000, timeout: 5000,
  });
  const state = { label, socket, frames: [], resets: [], disconnects: [], errors: [], connects: 0, keys: new Map(), subscribeLog: [], ackFrames: true, waiters: [] };
  const notify = () => { for (const waiter of [...state.waiters]) waiter(); };
  socket.on('connect', () => {
    state.connects++;
    // Resubscribe only the current key set after a reconnect.
    if (state.connects > 1) for (const key of state.keys.values()) subscribe(state, key, true);
    notify();
  });
  socket.on('connect_error', (error) => { state.errors.push({ message: error.message, code: error.data?.code ?? null, active: socket.active }); notify(); });
  socket.on('disconnect', (reason) => { state.disconnects.push(reason); notify(); });
  socket.on('campaign:invalidate', (payload, ack) => {
    state.frames.push({ at: Date.now(), payload });
    if (state.ackFrames && typeof ack === 'function') ack();
    else if (typeof ack === 'function') state.heldAcks = [...(state.heldAcks ?? []), ack];
    notify();
  });
  socket.on('campaign:reset', (payload) => { state.resets.push(payload); notify(); });
  return state;
}
function until(state, predicate, ms) {
  return new Promise((resolve) => {
    if (predicate()) return resolve(true);
    const timer = setTimeout(() => { state.waiters = state.waiters.filter((w) => w !== waiter); resolve(false); }, ms);
    const waiter = () => { if (predicate()) { clearTimeout(timer); state.waiters = state.waiters.filter((w) => w !== waiter); resolve(true); } };
    state.waiters.push(waiter);
  });
}
async function poll(predicate, ms) {
  const deadline = Date.now() + ms;
  while (Date.now() < deadline) { if (predicate()) return true; await sleep(100); }
  return predicate();
}
const connected = (state, ms = 5000) => until(state, () => state.socket.connected, ms);
const rejected = (state, ms = 5000) => until(state, () => state.errors.length > 0, ms);
async function subscribe(state, key, reconnect = false) {
  const payload = typeof key === 'object' && key !== null ? { ...key } : key;
  let ack;
  try { ack = await state.socket.timeout(5000).emitWithAck('campaign:subscribe', payload); } catch (error) { ack = { timeout: String(error.message) }; }
  state.subscribeLog.push({ key: payload, ack, reconnect });
  if (ack?.success && !reconnect) state.keys.set(`${key.family}:${key.campaignId}`, key);
  for (const waiter of [...state.waiters]) waiter();
  return ack;
}
async function unsubscribe(state, key) {
  state.keys.delete(`${key.family}:${key.campaignId}`);
  try { return await state.socket.timeout(5000).emitWithAck('campaign:unsubscribe', key); } catch (error) { return { timeout: String(error.message) }; }
}
const framesFor = (state, family) => state.frames.filter((f) => f.payload.family === family);
const head = (frame) => (frame ? BigInt(frame.payload.headVersion) : -1n);
const lastHead = (state, family) => head(framesFor(state, family).at(-1));
function close(...states) { for (const state of states) { state.socket.removeAllListeners(); state.socket.disconnect(); } }
function receipts() {
  return psql(`SELECT json_build_object('receipts',(SELECT count(*) FROM consumer_receipts),'published',count(*) FILTER (WHERE published_at IS NOT NULL),'leased',count(*) FILTER (WHERE lease_owner IS NOT NULL),'attempts',coalesce(sum(attempts),0),'dead',count(*) FILTER (WHERE dead_lettered_at IS NOT NULL),'events',count(*)) FROM durable_outbox`);
}
function latestEvent(topic) {
  return psql(`SELECT id::text FROM durable_outbox WHERE topic='${topic}' AND aggregate_id LIKE '["campaign","${config.campaignId}",%' ORDER BY occurred_at DESC, aggregate_version DESC LIMIT 1`);
}
const notifyEvent = (id) => psql(`SELECT pg_notify('campaign_outbox_wakeup', '{"schemaVersion":1,"eventId":"${id}"}')`);
const listeners = () => Number(psql(`SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND query LIKE 'LISTEN campaign_outbox_wakeup%'`));



const locks = [];
function lockStreams() {
  const proc = spawn(config.psql, ['-X', config.databaseUrl, '-v', 'ON_ERROR_STOP=1', '-At'], { stdio: ['pipe', 'pipe', 'pipe'] });
  let out = '';
  proc.stdout.on('data', (chunk) => { out += chunk.toString(); });
  proc.stderr.on('data', () => {});
  proc.stdin.write("BEGIN; LOCK TABLE outbox_streams IN ACCESS EXCLUSIVE MODE; SELECT 'R1_LOCKED';\n");
  locks.push(proc);
  return { ready: () => poll(() => out.includes('R1_LOCKED'), 3000), release: () => proc.stdin.end('COMMIT;\n\\q\n') };
}
const blockedReads = () => Number(psql("SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE '%COALESCE%outbox_streams%'"));
function stateVersion(value) {
  if (!value || typeof value !== 'object') return null;
  if (typeof value.stateVersion === 'string' || typeof value.stateVersion === 'number') return String(value.stateVersion);
  for (const child of Object.values(value)) { const found = stateVersion(child); if (found !== null) return found; }
  return null;
}
async function mutate(label, call) {
  const response = await call();
  if (response.status >= 300) throw new Error(`fixture ${label} failed: ${response.status}`);
  notifyEvent(latestEvent('campaign.interaction.changed'));
  return response;
}

async function main() {
  await startApi({ CAMPAIGN_PUSH_SWEEP_MS: '60000' });
  const key = { campaignId: config.campaignId, family: 'interaction' };
  const probe = client(undefined, 'pending-cancellation');
  const rep = client(undefined, 'replacement');
  const witness = client(undefined, 'witness');
  try {
    for (const s of [probe, rep, witness]) if (!await connected(s)) throw new Error(`${s.label} connection failed`);
    check('fixture: witness subscribes the same key normally', (await subscribe(witness, key)).success === true);

    // 1. blocked subscribe -> acknowledged unsubscribe -> late completion
    let lock = lockStreams();
    if (!await lock.ready()) throw new Error('fixture lock not acquired');
    const pending = probe.socket.timeout(6000).emitWithAck('campaign:subscribe', key);
    if (!await poll(() => blockedReads() > 0, 2000)) throw new Error('subscription read did not block');
    check('fixture: actual API subscription is blocked in durable-head read', true);
    const cancelled = await probe.socket.timeout(1500).emitWithAck('campaign:unsubscribe', key);
    check('fixture: unsubscribe is acknowledged while subscribe remains blocked', cancelled.success === true, { cancelled });
    lock.release();
    const late = await pending;
    check('LATE_SUBSCRIBE_ACK_REPORTS_CANCELLED', late.success === false && late.code === 'cancelled', { late });
    const joinedA = await mutate('join A', () => join(config.tokens.a, config.campaignId, '0'));
    const witnessFirst = await until(witness, () => witness.frames.length >= 1, 5000);
    check('fixture: witness receives the genuine mutation hint', witnessFirst, { frames: witness.frames.map((f) => f.payload) });
    await sleep(1000);
    check('ACKNOWLEDGED_UNSUBSCRIBE_MUST_PREVENT_LATE_ACTIVATION_AND_DELIVERY', probe.frames.length === 0, { unexpectedFrames: probe.frames.map((f) => f.payload) });

    // 2. unsubscribe -> replacement subscribe before the old observation completes
    lock = lockStreams();
    if (!await lock.ready()) throw new Error('fixture lock not acquired (replacement)');
    const oldOp = rep.socket.timeout(6000).emitWithAck('campaign:subscribe', key);
    if (!await poll(() => blockedReads() >= 1, 2000)) throw new Error('old subscription read did not block');
    const repCancel = await rep.socket.timeout(1500).emitWithAck('campaign:unsubscribe', key);
    const newOp = rep.socket.timeout(6000).emitWithAck('campaign:subscribe', key);
    const bothBlocked = await poll(() => blockedReads() >= 2, 2000);
    check('fixture: old subscription unsubscribed while blocked', repCancel.success === true, { repCancel });
    // A surviving stale reservation makes the server refuse the replacement at
    // once instead of starting its observation.
    check('REPLACEMENT_RESERVATION_ACCEPTED_WHILE_OLD_BLOCKED', bothBlocked, { blockedReads: blockedReads() });
    lock.release();
    const [oldAck, newAck] = await Promise.all([oldOp, newOp]);
    check('OLD_OPERATION_ACK_REPORTS_CANCELLED', oldAck.success === false && oldAck.code === 'cancelled', { oldAck });
    check('REPLACEMENT_SUBSCRIPTION_SUCCEEDS', newAck.success === true, { newAck });
    const witnessBefore = witness.frames.length;
    await mutate('join B', () => join(config.tokens.b, config.campaignId, '0'));
    const repHint = await until(rep, () => rep.frames.length >= 1, 5000);
    await sleep(1000);
    check('REPLACEMENT_RECEIVES_EXACTLY_ONE_HINT', repHint && rep.frames.length === 1, { frames: rep.frames.map((f) => f.payload) });
    check('fixture: witness receives the second hint', witness.frames.length === witnessBefore + 1, { frames: witness.frames.map((f) => f.payload) });
    check('CANCELLED_CONNECTION_STAYS_SILENT_AFTER_REPLACEMENT', probe.frames.length === 0, { unexpectedFrames: probe.frames.map((f) => f.payload) });

    // 3. repeated unsubscribe is idempotent and silences the key
    const repeats = [];
    for (let i = 0; i < 3; i++) repeats.push(await rep.socket.timeout(1500).emitWithAck('campaign:unsubscribe', key));
    check('REPEATED_UNSUBSCRIBE_SUCCEEDS', repeats.every((ack) => ack.success === true), { repeats });
    const version = stateVersion(joinedA.json);
    if (version === null) throw new Error('join response carried no stateVersion');
    const repBefore = rep.frames.length;
    await mutate('leave A', () => leave(config.tokens.a, config.campaignId, version));
    const witnessThird = await until(witness, () => witness.frames.length >= witnessBefore + 2, 5000);
    await sleep(1000);
    check('fixture: witness receives the third hint', witnessThird);
    check('UNSUBSCRIBED_KEYS_RECEIVE_NOTHING', rep.frames.length === repBefore && probe.frames.length === 0, { rep: rep.frames.map((f) => f.payload), probe: probe.frames.map((f) => f.payload) });

    // 4. disconnect while a subscribe is blocked; the server keeps serving others
    const gone = client(undefined, 'disconnect-during-observation');
    if (!await connected(gone)) throw new Error('disconnect probe connection failed');
    lock = lockStreams();
    if (!await lock.ready()) throw new Error('fixture lock not acquired (disconnect)');
    gone.socket.emit('campaign:subscribe', key, () => {});
    if (!await poll(() => blockedReads() >= 1, 2000)) throw new Error('disconnect probe read did not block');
    gone.socket.disconnect();
    lock.release();
    const fresh = client(undefined, 'after-disconnect');
    const freshOk = await connected(fresh) && (await subscribe(fresh, key)).success === true;
    check('SERVER_HEALTHY_AFTER_DISCONNECT_DURING_OBSERVATION', freshOk);
    close(fresh, gone);
    const health = await fetch(`${BASE}/healthz`);
    check('fixture: API healthy at end', health.ok);
  } finally {
    for (const proc of locks) if (proc.exitCode === null) { try { proc.stdin.end('ROLLBACK;\n\\q\n'); } catch { /* closed */ } }
    close(probe, rep, witness);
  }
}
main().catch((error) => check('fixture: probe setup completed without exception', false, { error: redact(error.stack || error) })).finally(async () => {
  for (const proc of locks) if (proc.exitCode === null) proc.kill('SIGTERM');
  const stopped = await stopApi('SIGTERM');
  check('fixture: API exits cleanly on SIGTERM', stopped && stopped.code === 0, { stopped });
  const failed = results.filter((r) => !r.pass);
  fs.writeFileSync(config.resultFile, JSON.stringify({ finishedAt: new Date().toISOString(), passed: results.length - failed.length, failed: failed.length,
    setupFailures: failed.filter((r) => r.name.startsWith('fixture:')).map((r) => r.name), invariantFailures: failed.filter((r) => !r.name.startsWith('fixture:')).map((r) => r.name), checks: results }, null, 2) + '\n');
  process.exit(failed.length ? 1 : 0);
});
