#!/usr/bin/env python3
"""Fail a `go test -json` run that skipped or lost mandatory tests.

An integration test that cannot find its database skips and the package still
passes, so a misconfigured job looks green. This guard reads the JSON event
stream and requires every --require test to have passed, every other skip to be
listed with --allow-skip, and no test or package to have failed.

Usage:
  go test -json ./internal/campaign/eventpush > events.jsonl
  python3 scripts/strict/require-go-tests.py events.jsonl \
    --require internal/campaign/eventpush.TestMigratedPush... \
    --allow-skip internal/campaign/eventpush.TestPushCostAtSubscriptionCap
  python3 scripts/strict/require-go-tests.py --self-test

Test names are `<package suffix>.<TopLevelTest>`; the suffix matches the end of
the Go import path. Subtests are ignored.
"""
import argparse
import json
import sys


def outcomes(lines):
    tests, packages = {}, {}
    for line in lines:
        try:
            event = json.loads(line)
        except ValueError:
            continue
        action, package, test = event.get('Action'), event.get('Package', ''), event.get('Test')
        if action not in ('pass', 'fail', 'skip'):
            continue
        if test is None:
            packages[package] = action
        elif '/' not in test:
            tests[(package, test)] = action
    return tests, packages


def matches(name, package, test):
    suffix, _, wanted = name.rpartition('.')
    return wanted == test and (package == suffix or package.endswith('/' + suffix))


def check(lines, required, allowed):
    tests, packages = outcomes(lines)
    problems = []
    if not tests and not packages:
        problems.append('no go test -json events found')
    for package, action in sorted(packages.items()):
        if action == 'fail':
            problems.append(f'package failed: {package}')
    for (package, test), action in sorted(tests.items()):
        if action == 'fail':
            problems.append(f'test failed: {package}.{test}')
        elif action == 'skip' and not any(matches(name, package, test) for name in allowed):
            problems.append(f'unexpected skip: {package}.{test}')
    for name in required:
        found = [action for (package, test), action in tests.items() if matches(name, package, test)]
        if not found:
            problems.append(f'required test did not run: {name}')
        elif found != ['pass']:
            problems.append(f'required test did not pass: {name} ({", ".join(found)})')
    return problems


def self_test():
    def stream(*events):
        return [json.dumps(e) for e in events]
    pkg = 'example.com/m/internal/campaign/eventpush'
    ok = stream({'Action': 'pass', 'Package': pkg, 'Test': 'TestRecovery'}, {'Action': 'skip', 'Package': pkg, 'Test': 'TestLoad'},
                {'Action': 'pass', 'Package': pkg, 'Test': 'TestRecovery/sub'}, {'Action': 'pass', 'Package': pkg})
    req, allow = ['internal/campaign/eventpush.TestRecovery'], ['internal/campaign/eventpush.TestLoad']
    cases = [
        (ok, req, allow, 0),
        (stream({'Action': 'skip', 'Package': pkg, 'Test': 'TestRecovery'}, {'Action': 'pass', 'Package': pkg}), req, allow, 2),
        (stream({'Action': 'pass', 'Package': pkg, 'Test': 'TestOther'}, {'Action': 'pass', 'Package': pkg}), req, allow, 1),
        (ok, req, [], 1),
        (stream({'Action': 'pass', 'Package': pkg, 'Test': 'TestRecovery'}, {'Action': 'fail', 'Package': pkg, 'Test': 'TestX'}, {'Action': 'fail', 'Package': pkg}), req, allow, 2),
        (['not json', ''], req, allow, 2),
        (stream({'Action': 'pass', 'Package': 'example.com/m/internal/campaign/eventpushx', 'Test': 'TestRecovery'}), req, allow, 1),
    ]
    for index, (lines, required, allowed, expected) in enumerate(cases):
        got = len(check(lines, required, allowed))
        if got != expected:
            print(f'self-test case {index}: {got} problems, expected {expected}', file=sys.stderr)
            return 1
    print('require-go-tests: self-test passed')
    return 0


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('events', nargs='?')
    parser.add_argument('--require', action='append', default=[])
    parser.add_argument('--allow-skip', action='append', default=[])
    parser.add_argument('--self-test', action='store_true')
    args = parser.parse_args()
    if args.self_test:
        return self_test()
    if not args.events:
        parser.error('events file required')
    with open(args.events, encoding='utf-8') as handle:
        problems = check(handle.read().splitlines(), args.require, args.allow_skip)
    for problem in problems:
        print(f'FAIL: {problem}', file=sys.stderr)
    if not problems:
        print(f'require-go-tests: {len(args.require)} required test(s) passed; no unexpected skips')
    return 1 if problems else 0


if __name__ == '__main__':
    sys.exit(main())
