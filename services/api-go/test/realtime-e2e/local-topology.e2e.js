/**
 * Local strict-zero-Nest TOPOLOGY proof (cross-process): asserts that a frame
 * received by a real socket.io-client from the gateway-only cmd/api was computed
 * by a SEPARATE cmd/market-stream worker process and fanned out via Redis — the
 * production topology (MARKET_STREAM_RUN_IN_API=false) with NO NestJS process.
 *
 * Unlike client.e2e.js (which uses a control endpoint to trigger broadcasts), here
 * the ticker frame originates from the real worker computing a DB-backed snapshot.
 */
'use strict';

const path = require('path');

function loadIo() {
  const cands = [];
  if (process.env.WEB_NM) cands.push(path.join(process.env.WEB_NM, 'socket.io-client'));
  cands.push('socket.io-client');
  for (const c of cands) {
    try {
      return require(c).io;
    } catch (_) {
      /* next */
    }
  }
  throw new Error('socket.io-client@4 not resolvable; set WEB_NM=apps/web/node_modules');
}

const io = loadIo();
const BASE = process.env.BASE || 'http://127.0.0.1:8097';
const SYMBOL = process.env.SYMBOL || 'ETH-USD';
const CHAIN = Number(process.env.CHAIN || 11155111);

let failures = 0;
const ok = (c, m) => {
  if (c) console.log('  PASS  ' + m);
  else {
    failures += 1;
    console.error('  FAIL  ' + m);
  }
};

function connect() {
  return new Promise((res, rej) => {
    const s = io(`${BASE}/markets`, {
      path: '/socket.io',
      transports: ['websocket'],
      reconnection: true,
      timeout: 6000,
      forceNew: true,
    });
    const t = setTimeout(() => rej(new Error('connect timeout')), 7000);
    s.on('connect', () => {
      clearTimeout(t);
      res(s);
    });
    s.on('connect_error', (e) => {
      clearTimeout(t);
      rej(e);
    });
  });
}
function emitAck(s, ev, p, ms = 5000) {
  return new Promise((res, rej) => {
    const t = setTimeout(() => rej(new Error('ack timeout ' + ev)), ms);
    s.emit(ev, p, (a) => {
      clearTimeout(t);
      res(a);
    });
  });
}
function nextEvent(s, ev, ms = 10000) {
  return new Promise((res, rej) => {
    const t = setTimeout(() => rej(new Error('event timeout ' + ev)), ms);
    s.once(ev, (p) => {
      clearTimeout(t);
      res(p);
    });
  });
}

(async () => {
  try {
    const s = await connect();
    ok(true, 'connected to gateway-only cmd/api /markets (websocket)');
    const frameP = nextEvent(s, `market:ticker:${SYMBOL}`, 12000);
    const ack = await emitAck(s, 'subscribe:market:ticker', { symbol: SYMBOL, chainId: CHAIN });
    ok(ack && ack.success === true, 'subscribe ack success from gateway-only API');
    const f = await frameP;
    ok(
      f && f.symbol === SYMBOL,
      `received ${SYMBOL} ticker computed by the SEPARATE cmd/market-stream worker + Redis fan-out (cross-process, no NestJS)`,
    );
    s.disconnect();
  } catch (e) {
    failures += 1;
    console.error('  FAIL  ' + (e && e.message ? e.message : e));
  }
  if (failures === 0) {
    console.log('\nLOCAL STRICT TOPOLOGY (cross-process realtime) PASSED');
    process.exit(0);
  } else {
    console.error(`\n${failures} CHECK(S) FAILED`);
    process.exit(1);
  }
})();
