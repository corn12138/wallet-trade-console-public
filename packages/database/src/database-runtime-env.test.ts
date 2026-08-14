import fs from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { assertDatabaseRuntimeEnvReady, prepareDatabaseRuntimeEnv } from './database-runtime-env';

const ORIGINAL_ENV = { ...process.env };
const ORIGINAL_CWD = process.cwd();
const createdWorkspaces: string[] = [];

function clearDatabaseEnv() {
  for (const key of [
    'DATABASE_URL',
    'DATABASE_HOST',
    'DATABASE_PORT',
    'DATABASE_NAME',
    'DATABASE_USER',
    'DATABASE_PASSWORD',
    'DATABASE_PASSWORD_BASE64',
    'DATABASE_PASSWORD_ENCODING',
    'DATABASE_SSL',
    'INIT_CWD',
    'npm_package_name',
  ]) {
    delete process.env[key];
  }
}

async function createWorkspace() {
  const workspaceRoot = await fs.mkdtemp(path.join(os.tmpdir(), 'ai-code-db-env-'));
  createdWorkspaces.push(workspaceRoot);
  await fs.writeFile(path.join(workspaceRoot, 'pnpm-workspace.yaml'), 'packages:\n  - "apps/*"\n');
  await fs.mkdir(path.join(workspaceRoot, 'apps', 'blog'), { recursive: true });
  return workspaceRoot;
}

describe('prepareDatabaseRuntimeEnv', () => {
  beforeEach(() => {
    process.env = { ...ORIGINAL_ENV };
    clearDatabaseEnv();
  });

  afterEach(async () => {
    process.env = { ...ORIGINAL_ENV };
    process.chdir(ORIGINAL_CWD);
    await Promise.all(createdWorkspaces.splice(0).map((workspaceRoot) => fs.rm(workspaceRoot, { recursive: true, force: true })));
  });

  it('loads local env files in development and synthesizes DATABASE_URL', async () => {
    const workspaceRoot = await createWorkspace();
    await fs.writeFile(
      path.join(workspaceRoot, 'apps', 'blog', '.env.local'),
      [
        'DATABASE_HOST=127.0.0.1',
        'DATABASE_PORT=6543',
        'DATABASE_NAME=journal_dev',
        'DATABASE_USER=journal_user',
        'DATABASE_PASSWORD=journal_password',
        'DATABASE_SSL=false',
      ].join('\n'),
    );

    process.chdir(workspaceRoot);
    process.env.NODE_ENV = 'development';
    process.env.INIT_CWD = workspaceRoot;

    const databaseUrl = prepareDatabaseRuntimeEnv();

    expect(databaseUrl).toBe('postgresql://journal_user:journal_password@127.0.0.1:6543/journal_dev?schema=public');
    expect(process.env.DATABASE_URL).toBe(databaseUrl);
  });

  it('does not load repo env files in production', async () => {
    const workspaceRoot = await createWorkspace();
    await fs.writeFile(
      path.join(workspaceRoot, 'apps', 'blog', '.env.production'),
      [
        'DATABASE_HOST=127.0.0.1',
        'DATABASE_PORT=6543',
        'DATABASE_NAME=should_not_load',
        'DATABASE_USER=should_not_load',
        'DATABASE_PASSWORD=should_not_load',
      ].join('\n'),
    );

    process.chdir(workspaceRoot);
    process.env.NODE_ENV = 'production';
    process.env.INIT_CWD = workspaceRoot;

    const databaseUrl = prepareDatabaseRuntimeEnv();

    expect(databaseUrl).toBeUndefined();
    expect(process.env.DATABASE_HOST).toBeUndefined();
    expect(process.env.DATABASE_URL).toBeUndefined();
  });

  it('fails fast in production when no database configuration is present', () => {
    process.env.NODE_ENV = 'production';

    expect(() => assertDatabaseRuntimeEnvReady()).toThrow(
      'DATABASE_URL is not configured for production.',
    );
  });

  it('accepts explicit production database configuration without loading repo env files', () => {
    process.env.NODE_ENV = 'production';
    process.env.DATABASE_HOST = 'db.internal';
    process.env.DATABASE_PORT = '5432';
    process.env.DATABASE_NAME = 'journal_prod';
    process.env.DATABASE_USER = 'journal_user';
    process.env.DATABASE_PASSWORD = 'journal_password';

    const databaseUrl = assertDatabaseRuntimeEnvReady();

    expect(databaseUrl).toBe('postgresql://journal_user:journal_password@db.internal:5432/journal_prod?schema=public');
    expect(process.env.DATABASE_URL).toBe(databaseUrl);
  });
});
