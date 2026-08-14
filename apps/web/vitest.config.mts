import path from 'node:path';
import { fileURLToPath } from 'node:url';
import react from '@vitejs/plugin-react';
import { configDefaults, defineConfig } from 'vitest/config';

const __dirname = path.dirname(fileURLToPath(import.meta.url));

export default defineConfig({
  plugins: [react()],
  cacheDir: '.vitest',
  test: {
    name: 'wallet-trade-console-web',
    globals: true,
    environment: 'happy-dom',
    setupFiles: ['./vitest.setup.ts'],
    include: ['src/**/*.{test,spec}.{ts,tsx}'],
    exclude: [
      ...configDefaults.exclude,
      '.next',
      'coverage',
      'node_modules',
    ],
    passWithNoTests: true,
    testTimeout: 10000,
    hookTimeout: 10000,
  },
  resolve: {
    alias: {
      '@': path.resolve(__dirname, './src'),
      '@wallet-trade/shared': path.resolve(__dirname, '../../packages/shared/src/index.ts'),
      '@wallet-trade/shared/node': path.resolve(__dirname, '../../packages/shared/src/node.ts'),
    },
  },
  define: {
    'process.env.NODE_ENV': '"test"',
  },
});
