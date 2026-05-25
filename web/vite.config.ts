import { sveltekit } from '@sveltejs/kit/vite';
import { defineConfig } from 'vite';

const gatewayURL = process.env.TALON_GATEWAY_URL ?? 'ws://127.0.0.1:18789';

export default defineConfig({
	plugins: [sveltekit()],
	server: {
		// Bind to all interfaces so phones on the same LAN can hit the
		// dev server (matches the "mobile is equal weight" rule).
		host: true,
		strictPort: false,
		proxy: {
			'/ws': {
				target: gatewayURL,
				ws: true,
				changeOrigin: true,
			},
			'/healthz': {
				target: gatewayURL.replace(/^ws/, 'http'),
				changeOrigin: true,
			},
			// Connect RPC routes: /talon.v1.<Service>/<Method>. Proxy
			// to the gateway over HTTP so the typed clients in
			// lib/gateway/connect.ts can talk to the running gateway
			// from `vite dev` without CORS gymnastics.
			'/talon.v1.': {
				target: gatewayURL.replace(/^ws/, 'http'),
				changeOrigin: true,
			},
		},
	},
});
