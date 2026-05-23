import { expect, test } from '@playwright/test';
import { readFileSync } from 'node:fs';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const srcDir = resolve(dirname(fileURLToPath(import.meta.url)), '../src');

test('App route sections are decomposed into focused Svelte 5 components', () => {
  const appSource = readFileSync(resolve(srcDir, 'App.svelte'), 'utf8');
  const watchSource = readFileSync(resolve(srcDir, 'routes/WatchRoute.svelte'), 'utf8');
  const settingsSource = readFileSync(resolve(srcDir, 'routes/SettingsDiagnosticsPanel.svelte'), 'utf8');

  expect(appSource).toContain("import WatchRoute from './routes/WatchRoute.svelte';");
  expect(appSource).toContain("import SettingsDiagnosticsPanel from './routes/SettingsDiagnosticsPanel.svelte';");
  expect(watchSource).toContain('$props(');
  expect(settingsSource).toContain('$props(');
  expect(watchSource).not.toMatch(/\son:[a-z]/);
  expect(settingsSource).not.toMatch(/\son:[a-z]/);
});
