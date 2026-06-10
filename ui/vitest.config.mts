import tsConfigPaths from 'vite-tsconfig-paths';
import { defineConfig } from 'vitest/config';

// vitest does not apply the tsconfig path aliases (e.g. @ui/*) from
// vite.config.mts; this config makes them available to tests
export default defineConfig({ plugins: [tsConfigPaths()] });
