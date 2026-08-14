import fs from 'node:fs';
import path from 'node:path';
import dotenv from 'dotenv';

function decodeBase64Value(value: string): string {
  return Buffer.from(value, 'base64').toString('utf8');
}

function resolveDatabasePassword(): string | undefined {
  const encodedPassword = process.env.DATABASE_PASSWORD_BASE64;
  if (encodedPassword && encodedPassword.trim().length > 0) {
    return decodeBase64Value(encodedPassword.trim());
  }

  const rawPassword = process.env.DATABASE_PASSWORD;
  if (!rawPassword || rawPassword.trim().length === 0) {
    return rawPassword;
  }

  const encoding = process.env.DATABASE_PASSWORD_ENCODING?.trim().toLowerCase();
  if (encoding === 'base64') {
    return decodeBase64Value(rawPassword.trim());
  }

  return rawPassword;
}

function resolveRepoRoot() {
  const start = path.resolve(process.env.INIT_CWD || process.cwd());
  let current = start;

  while (true) {
    if (fs.existsSync(path.join(current, 'pnpm-workspace.yaml'))) {
      return current;
    }

    const parent = path.dirname(current);
    if (parent === current) {
      return start;
    }

    current = parent;
  }
}

function resolveWorkspacePackageDirectory(repoRoot: string): string | null {
  const packageName = process.env.npm_package_name;
  if (!packageName || packageName === 'wallet-trade-console') {
    return null;
  }

  for (const scope of ['apps', 'services', 'packages']) {
    const candidate = path.join(repoRoot, scope, packageName);
    if (fs.existsSync(candidate) && fs.statSync(candidate).isDirectory()) {
      return candidate;
    }
  }

  return null;
}

function loadDatabaseEnvFiles(repoRoot: string): void {
  const envName = process.env.NODE_ENV || 'development';
  if (process.env.DATABASE_URL?.trim() || envName === 'production') {
    return;
  }

  const workspacePackageDirectory = resolveWorkspacePackageDirectory(repoRoot);
  const directories = [
    workspacePackageDirectory,
    process.cwd(),
    process.env.INIT_CWD,
    repoRoot,
    path.join(repoRoot, 'apps', 'web'),
    path.join(repoRoot, 'services', 'api'),
  ].filter((entry): entry is string => Boolean(entry));

  const seenDirectories = new Set<string>();
  const fileNames = [
    `.env.${envName}.local`,
    ...(envName === 'test' ? [] : ['.env.local']),
    `.env.${envName}`,
    '.env',
  ];

  for (const directory of directories) {
    const resolvedDirectory = path.resolve(directory);
    if (seenDirectories.has(resolvedDirectory)) {
      continue;
    }
    seenDirectories.add(resolvedDirectory);

    for (const fileName of fileNames) {
      const filePath = path.join(resolvedDirectory, fileName);
      if (!fs.existsSync(filePath) || !fs.statSync(filePath).isFile()) {
        continue;
      }

      dotenv.config({ path: filePath, override: false });
    }
  }
}

function ensureDatabaseUrl(): void {
  if (process.env.DATABASE_URL && process.env.DATABASE_URL.trim().length > 0) {
    return;
  }

  const host = process.env.DATABASE_HOST;
  const port = process.env.DATABASE_PORT || '6543';
  const name = process.env.DATABASE_NAME;
  const user = process.env.DATABASE_USER;
  const password = resolveDatabasePassword();
  const ssl = process.env.DATABASE_SSL === 'true';

  if (host && name && user && typeof password === 'string') {
    const auth = encodeURIComponent(user) + ':' + encodeURIComponent(password);
    const params = new URLSearchParams({ schema: 'public' });
    if (ssl) {
      params.append('sslmode', 'require');
    }

    process.env.DATABASE_URL = `postgresql://${auth}@${host}:${port}/${name}?${params.toString()}`;
  }
}

export function prepareDatabaseRuntimeEnv() {
  const repoRoot = resolveRepoRoot();
  loadDatabaseEnvFiles(repoRoot);
  ensureDatabaseUrl();

  return process.env.DATABASE_URL?.trim() || undefined;
}

export function assertDatabaseRuntimeEnvReady() {
  const databaseUrl = prepareDatabaseRuntimeEnv();

  if (process.env.NODE_ENV === 'production' && !databaseUrl) {
    throw new Error(
      'DATABASE_URL is not configured for production. Provide DATABASE_URL or DATABASE_HOST/DATABASE_PORT/DATABASE_NAME/DATABASE_USER and DATABASE_PASSWORD or DATABASE_PASSWORD_BASE64.',
    );
  }

  return databaseUrl;
}
