// ESLint flat config (fleetlint lint/configured/node). `make lint` runs it over
// the whole package; the pre-commit hook runs it on changed files. The
// frontend is plain JavaScript + Svelte 5, so the Svelte plugin replaces the
// TypeScript rule sets of fleetlint's template.
import js from '@eslint/js';
import svelte from 'eslint-plugin-svelte';
import globals from 'globals';

// Violations that predate the linter are frozen in eslint-suppressions.json
// (`npx eslint --suppress-all`): they do not fail the build, new ones do, and
// `npx eslint --prune-suppressions` drops entries as the code is cleaned up.

export default [
  { ignores: ['.svelte-kit/', 'node_modules/', 'coverage/'] },
  js.configs.recommended,
  ...svelte.configs.recommended,
  {
    languageOptions: { globals: { ...globals.browser, ...globals.node } },
    rules: {
      // The SPA is served at the origin root: no base path, so resolve() buys nothing.
      'svelte/no-navigation-without-resolve': 'off',
      'no-console': ['error', { allow: ['warn', 'error'] }],
      complexity: ['error', 15],
      'max-lines': ['error', { max: 800, skipBlankLines: true, skipComments: true }],
      'max-lines-per-function': ['error', { max: 80, skipBlankLines: true, skipComments: true }],
      'no-warning-comments': ['error', { terms: ['todo', 'fixme', 'xxx'] }]
    }
  },
  // CLI scripts print their output.
  { files: ['scripts/**'], rules: { 'no-console': 'off' } }
];
