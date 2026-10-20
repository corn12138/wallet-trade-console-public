#!/usr/bin/env python3
"""Campaign push cancellation e2e against the actual API, Socket.IO and PostgreSQL.

Builds the current cmd/api and the Campaign push fixture, clones a migrated
template into a disposable database, persists server-issued test sessions and
runs campaign-push-cancel-e2e.cjs. Runs the same way on a workstation and in CI.

Environment:
  CAMPAIGN_PUSH_E2E_ADMIN_URL  loopback PostgreSQL URL with CREATEDB rights (required)
  CAMPAIGN_PUSH_E2E_TEMPLATE   migrated template database name (required)
  CAMPAIGN_PUSH_E2E_OUT        directory for result.json, receipt.json and logs
                               (default: a new temporary directory, printed)
  PSQL                         psql binary (default: psql on PATH)
  GO_OVERLAY                   optional go build -overlay file for negative controls

Exit codes: 0 all checks passed; 1 a product invariant failed; 2 setup failed.
Evidence scope: synthetic sessions persisted in web3_auth_sessions and signed
with a per-run key; this is not real-wallet, SIWE or browser evidence.
"""
import base64, datetime, hashlib, hmac, json, os, re, secrets, shutil, signal, socket, subprocess, sys, tempfile, time, uuid
from pathlib import Path
from urllib.parse import parse_qsl, urlencode, urlsplit, urlunsplit

ROOT = Path(__file__).resolve().parents[2]
API = ROOT / 'services/api-go'
DRIVER = Path(__file__).with_suffix('.cjs')
CHAIN = 11155111
WALLETS = {'a': '0x' + 'a1' * 20, 'b': '0x' + 'b2' * 20, 'c': '0x' + 'c3' * 20}


def fail_setup(message):
    print(f'SETUP FAIL: {message}', file=sys.stderr)
    sys.exit(2)


def b64(value):
    return base64.urlsafe_b64encode(json.dumps(value, separators=(',', ':')).encode()).rstrip(b'=')


def token(secret, key, issuer, audience, wallet, sid, epoch, now):
    unsigned = b64({'alg': 'HS256', 'typ': 'JWT', 'kid': key}) + b'.' + b64({'sub': wallet, 'chainId': CHAIN, 'type': 'web3', 'sid': sid, 'sver': 1, 'epoch': epoch,
                                                                             'iss': issuer, 'aud': [audience], 'iat': now, 'exp': now + 3600})
    return (unsigned + b'.' + base64.urlsafe_b64encode(hmac.new(secret.encode(), unsigned, hashlib.sha256).digest()).rstrip(b'=')).decode()


def free_port():
    with socket.socket() as s:
        s.bind(('127.0.0.1', 0))
        return s.getsockname()[1]


def main():
    admin, template = os.environ.get('CAMPAIGN_PUSH_E2E_ADMIN_URL', ''), os.environ.get('CAMPAIGN_PUSH_E2E_TEMPLATE', '')
    parts = urlsplit(admin)
    if parts.hostname not in ('127.0.0.1', 'localhost', '::1') or not re.fullmatch(r'[a-z_][a-z0-9_]{0,62}', template):
        fail_setup('CAMPAIGN_PUSH_E2E_ADMIN_URL must be a loopback PostgreSQL URL and CAMPAIGN_PUSH_E2E_TEMPLATE a plain database name')
    psql = os.environ.get('PSQL') or shutil.which('psql')
    node = shutil.which('node')
    web_nm = ROOT / 'apps/web/node_modules'
    if not psql or not node or not (web_nm / 'socket.io-client').is_dir():
        fail_setup('psql, node and apps/web/node_modules/socket.io-client are required (run pnpm install)')
    out = Path(os.environ.get('CAMPAIGN_PUSH_E2E_OUT') or tempfile.mkdtemp(prefix='campaign-push-e2e-'))
    out.mkdir(parents=True, exist_ok=True)
    work = Path(tempfile.mkdtemp(prefix='campaign-push-e2e-work-'))
    password = parts.password or ''

    def sql(url, statement):
        return subprocess.run([psql, '-X', url, '-v', 'ON_ERROR_STOP=1', '-Atc', statement], check=True, capture_output=True, text=True).stdout.strip()

    overlay = os.environ.get('GO_OVERLAY')
    build = {}
    for name, package in [('api', './cmd/api'), ('campaignpushfixture', './test/campaignpushfixture')]:
        args = ['go', 'build', *(['-overlay', overlay] if overlay and name == 'api' else []), '-o', str(work / name), package]
        result = subprocess.run(args, cwd=API, capture_output=True, text=True)
        if result.returncode:
            (out / 'build.log').write_text(result.stdout + result.stderr)
            fail_setup(f'build {name} failed; see {out / "build.log"}')
        build[name] = hashlib.sha256((work / name).read_bytes()).hexdigest()
    sources = sorted((API / 'internal/campaign/eventpush').glob('*.go')) + [API / 'cmd/api/campaign.go']
    source_hashes = {str(p.relative_to(ROOT)): hashlib.sha256(p.read_bytes()).hexdigest() for p in sources}
    if overlay:
        source_hashes.update({f'overlay:{k}': hashlib.sha256(Path(v).read_bytes()).hexdigest() for k, v in json.loads(Path(overlay).read_text())['Replace'].items()})

    database = f'campaign_push_e2e_{os.getpid()}_{secrets.token_hex(3)}'
    url = urlunsplit((parts.scheme, parts.netloc, '/' + database, parts.query, ''))
    # The replacement proof holds two observations concurrently. pgx otherwise
    # sizes its pool from host CPUs, leaving only one push slot on small runners.
    api_query = dict(parse_qsl(parts.query))
    api_query['pool_max_conns'] = '16'
    api_url = urlunsplit((parts.scheme, parts.netloc, '/' + database, urlencode(api_query), ''))
    secret, key, issuer, audience = secrets.token_hex(32), 'push-e2e', 'local-push-e2e', 'local-push-e2e-api'
    pid_file = work / 'pids'
    pid_file.write_text('')
    record = {'startedAt': datetime.datetime.now(datetime.timezone.utc).isoformat(), 'template': template, 'apiPoolMaxConns': 16, 'goOverlay': overlay, 'build': build, 'sourceHashes': source_hashes,
              'scope': 'Actual cmd/api, Socket.IO (repository socket.io-client), migrated PostgreSQL clone, server-issued test sessions; NOTIFY sent in the publisher wire format; not wallet, SIWE or browser evidence.'}
    tokens = {}
    driver_code = None
    try:
        sql(admin, f'CREATE DATABASE "{database}" TEMPLATE "{template}"')
        record['postgresVersion'] = sql(url, 'SHOW server_version')
        now = int(time.time())
        for label in ('a', 'b'):
            sid = str(uuid.uuid4())
            sql(url, f"INSERT INTO web3_auth_sessions(id,wallet_address,chain_id,session_version,auth_epoch,key_id,issued_at,expires_at) "
                     f"VALUES ('{sid}','{WALLETS[label]}',{CHAIN},1,1,'{key}',to_timestamp({now}),to_timestamp({now + 3600}))")
            tokens[label] = token(secret, key, issuer, audience, WALLETS[label], sid, 1, now)
        seed = json.loads(subprocess.run([str(work / 'campaignpushfixture'), 'seed'], env={'PATH': os.environ['PATH'], 'DATABASE_URL': url},
                                         check=True, capture_output=True, text=True).stdout)
        config = {'port': free_port(), 'apiBinary': str(work / 'api'), 'fixtureBinary': str(work / 'campaignpushfixture'), 'psql': psql, 'databaseUrl': url,
                  'apiLog': str(out / 'api.log'), 'pidFile': str(pid_file), 'resultFile': str(out / 'result.json'), 'campaignId': seed['campaignId'],
                  'draftId': seed['draftId'], 'publicationVersion': seed['publicationVersion'], 'walletA': WALLETS['a'], 'walletB': WALLETS['b'], 'tokens': tokens,
                  'redact': [password] if password else [],
                  'apiEnv': {'NODE_ENV': 'test', 'DATABASE_URL': api_url, 'WEB3_JWT_ISSUER': issuer, 'WEB3_JWT_AUDIENCE': audience, 'WEB3_JWT_KEY_ID': key,
                             'WEB3_JWT_SECRET': secret, 'WEB3_AUTH_EPOCH': '1', 'REALTIME_ENABLED': 'true', 'CAMPAIGN_V2_ENABLED': 'true',
                             'MARKET_STREAM_ENABLED': 'false', 'LOG_LEVEL': 'info'}}
        env = {'PATH': os.environ['PATH'], 'HOME': os.environ.get('HOME', str(work)), 'PUSH_E2E_CONFIG_JSON': json.dumps(config), 'WEB_NM': str(web_nm)}
        with (out / 'driver.log').open('w') as log:
            driver_code = subprocess.run([node, str(DRIVER)], env=env, stdout=log, stderr=subprocess.STDOUT, timeout=600).returncode
        result = json.loads((out / 'result.json').read_text())
        record.update({'driverExitCode': driver_code, 'checks': len(result['checks']), 'passed': result['passed'], 'failed': result['failed'],
                       'setupFailures': result['setupFailures'], 'invariantFailures': result['invariantFailures']})
    except (subprocess.CalledProcessError, FileNotFoundError, json.JSONDecodeError, subprocess.TimeoutExpired) as error:
        record['setupError'] = type(error).__name__
    finally:
        stopped = []
        for line in pid_file.read_text().split() if pid_file.exists() else []:
            try:
                os.kill(int(line), signal.SIGKILL)
                stopped.append({'pid': int(line), 'state': 'killed-leftover'})
            except ProcessLookupError:
                stopped.append({'pid': int(line), 'state': 'already-exited'})
        record['ownedProcesses'] = stopped
        try:
            sql(admin, f'DROP DATABASE IF EXISTS "{database}" WITH (FORCE)')
            record['databaseDropped'] = sql(admin, f"SELECT count(*) FROM pg_database WHERE datname='{database}'") == '0'
        except subprocess.CalledProcessError:
            record['databaseDropped'] = False
        shutil.rmtree(work, ignore_errors=True)
        record['workDirectoryRemoved'] = not work.exists()
        leaked = [s for s in [secret, password, *tokens.values()] if s and any(s in p.read_text(errors='ignore') for p in out.iterdir() if p.is_file())]
        record['secretsInOutput'] = len(leaked)
        record['finishedAt'] = datetime.datetime.now(datetime.timezone.utc).isoformat()
        (out / 'receipt.json').write_text(json.dumps(record, indent=2) + '\n')
    print(json.dumps({k: record.get(k) for k in ('postgresVersion', 'checks', 'passed', 'failed', 'setupFailures', 'invariantFailures', 'databaseDropped', 'secretsInOutput')}))
    print(f'output: {out}')
    if record.get('setupError') or record.get('setupFailures') or not record.get('databaseDropped') or record.get('secretsInOutput'):
        sys.exit(2)
    sys.exit(0 if record.get('failed') == 0 and driver_code == 0 else 1)


if __name__ == '__main__':
    main()
