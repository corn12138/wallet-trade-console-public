/**
 * Strict zero-Nest realtime e2e — drives the Go Socket.IO tier
 * (internal/realtime, served by test/realtimeserver) with the repo's REAL
 * socket.io-client@4.8.3, proving EIO4 parity that a Go-only unit test cannot:
 *
 *   1. /markets  websocket transport: connect + subscribe ACK (success shape)
 *   2. /markets  guard rejection: invalid symbol -> {success:false, code:invalid_key}
 *   3. /markets  room fanout: ticker/book/trades frames (snapshot-on-subscribe + broadcast)
 *   4. /markets  polling transport: connect + subscribe ACK over XHR polling
 *   5. /markets  reconnect: transport drop -> automatic re-connect
 *   6. /token-events subscribe:token ACK (success + invalid) + trade fanout
 *   7. REST fallback: GET /api/markets/snapshot/{symbol} contract
 *
 * No test framework: plain assertions, process exits non-zero on first failure
 * batch. socket.io-client is resolved from apps/web/node_modules via WEB_NM.
 */
'use strict';

const path = require('path');

function loadIoClient() {
  const candidates = [];
  if (process.env.WEB_NM) candidates.push(path.join(process.env.WEB_NM, 'socket.io-client'));
  candidates.push('socket.io-client');
  for (const c of candidates) {
    try {
      return require(c).io;
    } catch (_) {
      /* try next */
    }
  }
  throw new Error('socket.io-client@4 not resolvable; set WEB_NM=apps/web/node_modules');
}

const io = loadIoClient();
const BASE = process.env.BASE || 'http://127.0.0.1:8099';
const SYMBOL = 'ETH-USD';
const CHAIN = 11155111;
const TOKEN = '0xAABBccddeeff00112233445566778899aAbBcCdD';
const TOKEN_LOWER = TOKEN.toLowerCase();

let failures = 0;
function ok(cond, msg) {
  if (cond) {
    console.log(`  PASS  ${msg}`);
  } else {
    failures += 1;
    console.error(`  FAIL  ${msg}`);
  }
}
function deepHas(obj, kv) {
  return obj && typeof obj === 'object' && Object.entries(kv).every(([k, v]) => obj[k] === v);
}

function connect(namespace, transports) {
  return new Promise((resolve, reject) => {
    const socket = io(`${BASE}${namespace}`, {
      path: '/socket.io',
      transports,
      reconnection: true,
      reconnectionAttempts: 5,
      reconnectionDelay: 100,
      timeout: 4000,
      forceNew: true,
    });
    const t = setTimeout(() => reject(new Error(`connect timeout ${namespace} [${transports}]`)), 5000);
    socket.on('connect', () => {
      clearTimeout(t);
      resolve(socket);
    });
    socket.on('connect_error', (e) => {
      clearTimeout(t);
      reject(e);
    });
  });
}

function emitAck(socket, event, payload, timeoutMs = 3000) {
  return new Promise((resolve, reject) => {
    const t = setTimeout(() => reject(new Error(`ack timeout for ${event}`)), timeoutMs);
    const cb = (ack) => {
      clearTimeout(t);
      resolve(ack);
    };
    if (payload === undefined) socket.emit(event, cb);
    else socket.emit(event, payload, cb);
  });
}

function nextEvent(socket, event, timeoutMs = 3000) {
  return new Promise((resolve, reject) => {
    const t = setTimeout(() => reject(new Error(`event timeout ${event}`)), timeoutMs);
    socket.once(event, (payload) => {
      clearTimeout(t);
      resolve(payload);
    });
  });
}

async function testMarketsWebsocket() {
  console.log('[1] /markets websocket: connect + subscribe ACK');
  const s = await connect('/markets', ['websocket']);
  ok(s.io.engine.transport.name === 'websocket', 'transport is websocket');

  const ack = await emitAck(s, 'subscribe:market:ticker', { symbol: SYMBOL, chainId: CHAIN });
  ok(deepHas(ack, { success: true, topic: 'ticker', symbol: SYMBOL, chainId: CHAIN }), 'ticker subscribe ack success shape');
  ok(ack.room === `market:ticker:${SYMBOL}:${CHAIN}`, `ack.room = ${ack && ack.room}`);
  ok(ack.event === `market:ticker:${SYMBOL}`, `ack.event = ${ack && ack.event}`);

  console.log('[2] /markets guard rejection: invalid symbol');
  const bad = await emitAck(s, 'subscribe:market:ticker', { symbol: '!!bad symbol!!' });
  ok(deepHas(bad, { success: false, code: 'invalid_key' }), 'invalid symbol -> invalid_key ack');

  console.log('[3] /markets room fanout: book + trades + broadcast');
  // Attach the frame listener BEFORE the subscribe emit, because
  // snapshot-on-subscribe delivers the first frame synchronously on subscribe.
  const bookFrameP = nextEvent(s, `market:book:${SYMBOL}`);
  const bookAck = await emitAck(s, 'subscribe:market:book', { symbol: SYMBOL, chainId: CHAIN });
  ok(deepHas(bookAck, { success: true, topic: 'book' }), 'book subscribe ack');
  const bookFrame = await bookFrameP;
  ok(Array.isArray(bookFrame.bids) && Array.isArray(bookFrame.asks), 'book snapshot-on-subscribe frame has bids/asks');

  const tradesFrameP = nextEvent(s, `market:trades:${SYMBOL}`);
  const tradesAck = await emitAck(s, 'subscribe:market:trades', { symbol: SYMBOL, chainId: CHAIN });
  ok(deepHas(tradesAck, { success: true, topic: 'trades' }), 'trades subscribe ack');
  const tradesFrame = await tradesFrameP;
  ok(Array.isArray(tradesFrame.trades), 'trades snapshot-on-subscribe frame has trades[]');

  // worker-style broadcast fan-out to the joined ticker room. NOTE: broadcast
  // frames are VOLATILE (drop under write backpressure, matching NestJS
  // `.volatile`), so asserting all three in one burst is flaky by design — the
  // per-topic room+event SHAPE is already proven above via the reliable
  // (non-volatile) snapshot-on-subscribe frames. Here we just prove the volatile
  // broadcast path reaches a subscribed room.
  const broadcastTicker = nextEvent(s, `market:ticker:${SYMBOL}`, 4000);
  await fetch(`${BASE}/control/broadcast`, { method: 'POST' });
  const bt = await broadcastTicker;
  ok(bt && bt.symbol === SYMBOL && bt.price === '1234.56', 'volatile broadcast ticker fanned out to subscribed room');

  console.log('[5] /markets reconnect: transport drop -> auto reconnect');
  let reconnected = false;
  const reconnectP = new Promise((resolve) => {
    s.io.once('reconnect', () => {
      reconnected = true;
      resolve();
    });
    setTimeout(resolve, 4000);
  });
  s.io.engine.close(); // simulate a transport drop
  await reconnectP;
  ok(reconnected, 'client auto-reconnected after transport drop');

  // unsubscribe ack shape
  const unsub = await emitAck(s, 'unsubscribe:market:ticker', { symbol: SYMBOL, chainId: CHAIN });
  ok(deepHas(unsub, { success: true }) && typeof unsub.room === 'string', 'unsubscribe ack {success,room}');

  s.disconnect();
}

async function testMarketsPolling() {
  console.log('[4] /markets polling transport: connect + subscribe ACK');
  const s = await connect('/markets', ['polling']);
  ok(s.io.engine.transport.name === 'polling', `transport is polling (got ${s.io.engine.transport.name})`);
  // Attach the frame listener BEFORE subscribing (snapshot-on-subscribe is sync).
  const frameP = nextEvent(s, `market:ticker:${SYMBOL}`);
  const ack = await emitAck(s, 'subscribe:market:ticker', { symbol: SYMBOL, chainId: CHAIN });
  ok(deepHas(ack, { success: true, symbol: SYMBOL }), 'polling subscribe ack success');
  const frame = await frameP;
  ok(frame.price === '1234.56', 'polling received ticker frame over XHR transport');
  s.disconnect();
}

async function testTokenEvents() {
  console.log('[6] /token-events: subscribe ACK (valid + invalid) + trade fanout');
  const s = await connect('/token-events', ['websocket']);

  const ack = await emitAck(s, 'subscribe:token', TOKEN);
  ok(deepHas(ack, { success: true, room: `token:${TOKEN_LOWER}`, tokenAddress: TOKEN_LOWER }), 'subscribe:token ack normalizes + success');

  const bad = await emitAck(s, 'subscribe:token', '0xnothex');
  ok(deepHas(bad, { success: false, code: 'invalid_key' }), 'invalid token -> invalid_key ack');

  const tradesAck = await emitAck(s, 'subscribe:trades', undefined);
  ok(deepHas(tradesAck, { success: true, room: 'all-trades' }), 'subscribe:trades ack {success,room:all-trades}');

  const tradeFrame = nextEvent(s, 'trade', 4000);
  await fetch(`${BASE}/control/broadcast-trade`, { method: 'POST' });
  const tf = await tradeFrame;
  ok(tf && tf.type === 'buy', 'token trade fanned out to subscriber');

  const unsubTrades = await emitAck(s, 'unsubscribe:trades', undefined);
  ok(deepHas(unsubTrades, { success: true }) && unsubTrades.room === undefined, 'unsubscribe:trades ack {success} (no room, NestJS parity)');

  s.disconnect();
}

async function testProducerPath() {
  console.log('[8] producer path: eventbus encode -> dispatch -> subscriber delivery');
  const s = await connect('/token-events', ['websocket']);
  await emitAck(s, 'subscribe:token', TOKEN);

  const publish = (kind, payload) =>
    fetch(`${BASE}/control/publish-token-event`, {
      method: 'POST',
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify({ kind, tokenAddress: TOKEN, payload }),
    });

  // trade — the shape the indexer sink publishes after a committed
  // token_trades insert.
  const tradeP = nextEvent(s, 'trade', 4000);
  let res = await publish('trade', {
    tokenAddress: TOKEN_LOWER,
    tokenId: 'tok-1',
    type: 'BUY',
    userAddress: '0x1111111111111111111111111111111111111111',
    tokenAmount: '1000000000000000000',
    ethAmount: '5000000000000000',
    price: 1.23,
    txHash: '0xabc',
    blockNumber: 42,
    timestamp: '2026-07-06T00:00:00Z',
  });
  ok(res.status === 204, `publish trade -> 204 (got ${res.status})`);
  const trade = await tradeP;
  ok(trade && trade.type === 'BUY' && trade.txHash === '0xabc', 'producer trade delivered to token room subscriber');

  const priceP = nextEvent(s, 'price-update', 4000);
  res = await publish('price-update', {
    tokenAddress: TOKEN_LOWER,
    price: 1.23,
    marketCap: 1230000000,
    timestamp: '2026-07-06T00:00:00Z',
  });
  ok(res.status === 204, `publish price-update -> 204 (got ${res.status})`);
  const price = await priceP;
  ok(price && price.price === 1.23, 'producer price-update delivered to token room subscriber');

  const gradP = nextEvent(s, 'graduation', 4000);
  res = await publish('graduation', {
    tokenAddress: TOKEN_LOWER,
    marketCap: '69000000000000000000000',
    lpToken: '0x3333333333333333333333333333333333333333',
    lpAmount: '1000',
    timestamp: '2026-07-06T00:00:00Z',
  });
  ok(res.status === 204, `publish graduation -> 204 (got ${res.status})`);
  const grad = await gradP;
  ok(grad && grad.lpToken === '0x3333333333333333333333333333333333333333', 'producer graduation delivered (reliable emit)');

  // Malformed producer payloads must be rejected, not broadcast.
  res = await fetch(`${BASE}/control/publish-token-event`, {
    method: 'POST',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify({ kind: '', tokenAddress: '', payload: {} }),
  });
  ok(res.status === 400, `malformed producer event rejected with 400 (got ${res.status})`);

  s.disconnect();
}

async function testRestFallback() {
  console.log('[7] REST fallback: GET /api/markets/snapshot/{symbol}');
  const res = await fetch(`${BASE}/api/markets/snapshot/${SYMBOL}`);
  ok(res.ok, `REST snapshot 200 (got ${res.status})`);
  const body = await res.json();
  ok(body.symbol === SYMBOL && body.ticker && body.orderbook && Array.isArray(body.trades), 'REST snapshot has {symbol,ticker,orderbook,trades}');
}

(async () => {
  try {
    await testMarketsWebsocket();
    await testMarketsPolling();
    await testTokenEvents();
    await testProducerPath();
    await testRestFallback();
  } catch (e) {
    failures += 1;
    console.error('  FAIL  unexpected error:', e && e.message ? e.message : e);
  }
  if (failures === 0) {
    console.log('\nALL REALTIME E2E CHECKS PASSED');
    process.exit(0);
  } else {
    console.error(`\n${failures} REALTIME E2E CHECK(S) FAILED`);
    process.exit(1);
  }
})();
