// Vitest (fleetlint quality/coverage-threshold/node). `make test` runs the
// tests, `make cover` adds coverage and fails below the floors. The UI has no
// component tests yet, so coverage is measured on the pure modules in src/lib
// that have tests; widen `include` as tests are added, never lower a floor.
import { defineConfig, mergeConfig } from 'vitest/config';
import viteConfig from './vite.config.js';

export default mergeConfig(
  viteConfig,
  defineConfig({
    test: {
      include: ['src/**/*.test.js'],
      environment: 'node',
      allowOnly: false,
      coverage: {
        provider: 'v8',
        include: [
          'src/lib/url.js',
          'src/lib/platform.js',
          'src/lib/media.js',
          'src/lib/exportGuides.js'
        ],
        reporter: ['text', 'lcov'],
        thresholds: { lines: 90, functions: 90, branches: 80, statements: 90 }
      }
    }
  })
);
