import { defineConfig } from 'vitest/config';
import { svelte } from '@sveltejs/vite-plugin-svelte';
import { playwright } from '@vitest/browser-playwright';

// Browser-mode test config (headless Chromium via Playwright). Kept separate
// from vite.config.ts on purpose: the app build needs the SvelteKit plugin +
// dev proxy, while tests only need the Svelte compiler (for runes / .svelte.ts
// stores) and a real browser. The chat store talks connect+json over fetch
// with streaming ReadableStream bodies, so a real browser — not jsdom — is the
// honest environment. Run with `pnpm test`.
export default defineConfig({
	plugins: [svelte()],
	test: {
		include: ['src/**/*.{test,spec}.{js,ts}'],
		browser: {
			enabled: true,
			provider: playwright(),
			headless: true,
			instances: [{ browser: 'chromium' }]
		}
	}
});
