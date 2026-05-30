import { afterEach, expect, test } from 'vitest';
import { makeChatStore } from './chatStore.svelte';

// Regression test for talon-15y: the chat composer wedged because
// startSubscribe ran the event stream once and never reconnected. When the
// gateway closed the stream (Sinks.Drain on restart, client disconnect, proxy
// timeout) the FE went deaf — send() still fired but its delta/final events
// never arrived, so status stayed 'streaming' and Enter silently no-op'd.
//
// This drives the real store over real connect+json framing in a headless
// browser. The first Subscribe response closes mid-run (mimicking a gateway
// restart); the fix must reconnect and resync history, landing status back on
// 'idle'. Against the pre-fix code this asserts subscribe === 1 and fails.

const SUBSCRIBE = '/talon.v1.ChatService/Subscribe';
const HISTORY = '/talon.v1.ChatService/History';

// connectFrame builds one connect-protocol envelope: a 1-byte flag, a 4-byte
// big-endian length, then the JSON payload. flag 0 = message, flag 2 =
// end-of-stream.
function connectFrame(flag: number, obj: unknown): Uint8Array {
	const payload = new TextEncoder().encode(JSON.stringify(obj));
	const out = new Uint8Array(5 + payload.length);
	out[0] = flag;
	new DataView(out.buffer).setUint32(1, payload.length, false);
	out.set(payload, 5);
	return out;
}

let realFetch: typeof globalThis.fetch | undefined;
afterEach(() => {
	if (realFetch) globalThis.fetch = realFetch;
	realFetch = undefined;
});

test('reconnects and reconciles after the event stream closes', async () => {
	const counts = { subscribe: 0, history: 0 };
	realFetch = globalThis.fetch;

	globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
		const url = typeof input === 'string' ? input : input instanceof URL ? input.href : input.url;

		if (url.includes(HISTORY)) {
			counts.history++;
			return new Response(JSON.stringify({ messages: [] }), {
				status: 200,
				headers: { 'content-type': 'application/json' }
			});
		}

		if (url.includes(SUBSCRIBE)) {
			counts.subscribe++;
			const first = counts.subscribe === 1;
			const body = new ReadableStream<Uint8Array>({
				start(controller) {
					if (first) {
						// Ready frame, then a clean end-of-stream: the gateway
						// closing the subscribe stream on restart.
						controller.enqueue(connectFrame(0, { sessionKey: 'test' }));
						controller.enqueue(connectFrame(2, {}));
						controller.close();
					}
					// Later attempts stay open so the reconnect loop blocks
					// there instead of spinning.
				}
			});
			return new Response(body, {
				status: 200,
				headers: { 'content-type': 'application/connect+json' }
			});
		}

		return realFetch!(input, init);
	}) as typeof globalThis.fetch;

	const store = makeChatStore('agent:test:reconnect');
	store.startSubscribe();

	// Wait for the reconnect (subscribe #2) after the ~500ms backoff.
	const deadline = performance.now() + 4000;
	while (counts.subscribe < 2 && performance.now() < deadline) {
		await new Promise((r) => setTimeout(r, 50));
	}
	store.dispose();

	expect(counts.subscribe, 'subscribe must be retried after the stream closes').toBeGreaterThanOrEqual(
		2
	);
	expect(counts.history, 'history must be re-fetched to reconcile on reconnect').toBeGreaterThanOrEqual(
		1
	);
	expect(store.status, 'status must not stay stuck on streaming').toBe('idle');
});
