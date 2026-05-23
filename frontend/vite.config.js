import { svelte } from '@sveltejs/vite-plugin-svelte';
import { defineConfig } from 'vite';

export default defineConfig({
  plugins: [svelte()],
  build: {
    outDir: '../internal/web/static',
    emptyOutDir: true,
    sourcemap: false,
  },
});
