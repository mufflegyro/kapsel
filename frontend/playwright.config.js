import { defineConfig, devices } from '@playwright/test';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const __dirname = dirname(fileURLToPath(import.meta.url));
const repoRoot = resolve(__dirname, '..');
const port = process.env.KAPSEL_E2E_PORT || '18080';
const baseURL = process.env.KAPSEL_E2E_BASE_URL || `http://127.0.0.1:${port}`;

export default defineConfig({
  testDir: './e2e',
  fullyParallel: false,
  workers: 1,
  timeout: 30_000,
  expect: { timeout: 5_000 },
  use: {
    baseURL,
    trace: 'retain-on-failure',
  },
  projects: [
    {
      name: 'desktop',
      use: { browserName: 'chromium', viewport: { width: 1280, height: 800 } },
    },
    {
      name: 'mobile',
      use: { ...devices['Pixel 5'], browserName: 'chromium' },
    },
  ],
  webServer: {
    command: 'go run ./internal/e2e/testserver',
    cwd: repoRoot,
    env: {
      KAPSEL_E2E_ADDR: `127.0.0.1:${port}`,
    },
    url: `${baseURL}/api/session`,
    reuseExistingServer: false,
    timeout: 120_000,
  },
});
