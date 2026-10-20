import { defineConfig } from 'vitest/config';
import { fileURLToPath } from 'node:url';

export default defineConfig({
  test: {
    environment: 'node',
    include: ['acceptance/*.acceptance.ts'],
    testTimeout: 120_000,
    hookTimeout: 120_000,
    passWithNoTests: false,
  },
  resolve: {
    alias: {
      '@': fileURLToPath(new URL('../src', import.meta.url)),
      '@wallet-trade/shared': fileURLToPath(new URL('../../../packages/shared/src/index.ts', import.meta.url)),
    },
  },
});
