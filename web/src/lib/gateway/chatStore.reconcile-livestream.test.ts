import { afterEach, expect, test } from 'vitest';
import { makeChatStore } from './chatStore.svelte';

// Regression: the chat UI showed the assistant thinking box, then it vanished
// and the response reappeared ~a second later in a different spot — instead of
// the thinking indicator persisting and the response landing in place.
//
// Root cause: send() armed a fixed 1800ms history-reconcile timer guarded only
// by `status === 'streaming'`. Any turn longer than 1.8s (i.e. essentially
// every real turn) tripped it MID-STREAM: loadHistory() replaced the whole
// messages array (wiping the optimistic streaming bubble + its thinking trace,
// keyed remount), then the eventual `final` appended a brand-new bubble at the
// end. The fix makes the reconcile liveness-aware — it resets on every received
// event, so an actively-streaming turn never reconciles mid-stream; only a
// genuinely silent (hung) stream does, after the silence threshold.

const SEND = '/talon.v1.ChatService/Send';
const SUBSCRIBE = '/talon.v1.ChatService/Subscribe';
const HISTORY = '/talon.v1.ChatService/History';

// connectFrame builds one connect-protocol envelope: 1-byte flag, 4-byte
// big-endian length, JSON payload. flag 0 = message.
function connectFrame(flag: number, obj: unknown): Uint8Array {
	const payload = new TextEncoder().encode(JSON.stringify(obj));
	const out = new Uint8Array(5 + payload.length);
	out[0] = flag;
	new DataView(out.buffer).setUint32(1, payload.length, false);
	out.set(payload, 5);
	return out;
}

// reqJSON decodes a connect-web request body, which arrives as a Uint8Array of
// the JSON message bytes (not a string).
function reqJSON(init?: RequestInit): Record<string, unknown> {
	const raw = init?.body;
	if (raw == null) return {};
	const text = typeof raw === 'string' ? raw : new TextDecoder().decode(raw as Uint8Array);
	return JSON.parse(text);
}

let realFetch: typeof globalThis.fetch | undefined;
afterEach(() => {
	if (realFetch) globalThis.fetch = realFetch;
	realFetch = undefined;
});

test('an actively-streaming turn longer than the old 1800ms window is not wiped by a mid-stream history reconcile', async () => {
	const sessionKey = 'agent:test:slowturn';
	const counts = { history: 0 };
	let controller: ReadableStreamDefaultController<Uint8Array> | null = null;
	let runId = '';

	realFetch = globalThis.fetch;
	globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
		const url = typeof input === 'string' ? input : input instanceof URL ? input.href : input.url;

		if (url.includes(SEND)) {
			const body = reqJSON(init);
			runId = String(body.idempotencyKey);
			// The turn is accepted and starts streaming reasoning, but never
			// finishes within the test window (a long, healthy turn). Push a
			// thinking event onto the open subscribe stream so the bubble shows
			// its reasoning trace.
			queueMicrotask(() => {
				controller?.enqueue(
					connectFrame(0, { runId, sessionKey, thinking: { cumulative: 'reasoning about it' } })
				);
			});
			return new Response(JSON.stringify({ runId }), {
				status: 200,
				headers: { 'content-type': 'application/json' }
			});
		}

		if (url.includes(HISTORY)) {
			counts.history++;
			// If a mid-stream reconcile wrongly fires, it replaces messages with
			// this sentinel server row — detectable by the assertions below.
			return new Response(
				JSON.stringify({
					messages: [{ id: 'srv-1', seq: 1, user: { text: 'hello' } }]
				}),
				{ status: 200, headers: { 'content-type': 'application/json' } }
			);
		}

		if (url.includes(SUBSCRIBE)) {
			const body = new ReadableStream<Uint8Array>({
				start(c) {
					controller = c;
					c.enqueue(connectFrame(0, { sessionKey })); // ready frame (no-op event)
					// Stays open: the turn is still streaming.
				}
			});
			return new Response(body, {
				status: 200,
				headers: { 'content-type': 'application/connect+json' }
			});
		}

		return realFetch!(input, init);
	}) as typeof globalThis.fetch;

	const store = makeChatStore(sessionKey);
	store.startSubscribe();

	// Wait for the subscribe stream to open (controller captured).
	const openBy = performance.now() + 2000;
	while (!controller && performance.now() < openBy) await new Promise((r) => setTimeout(r, 10));
	expect(controller, 'subscribe stream should open').toBeTruthy();

	await store.send('hello');

	// Wait past the old fixed 1800ms reconcile window while the stream stays
	// open and active. On the buggy code, loadHistory fires at ~1800ms here.
	await new Promise((r) => setTimeout(r, 2300));
	store.dispose();

	const assistants = store.messages.filter((m) => m.role === 'assistant');
	expect(assistants.length, 'exactly one assistant bubble (not wiped + re-appended)').toBe(1);
	expect(assistants[0]?.thinking, 'the streamed thinking trace must survive').toContain('reasoning');
	expect(
		store.messages.some((m) => m.id === 'srv-1'),
		'no mid-stream history reconcile should have replaced the live bubble'
	).toBe(false);
	expect(counts.history, 'history must NOT be re-fetched mid-stream on a healthy turn').toBe(0);
});

test('a silent (hung) stream still reconciles from history after the silence threshold', async () => {
	const sessionKey = 'agent:test:hung';
	const counts = { history: 0 };

	realFetch = globalThis.fetch;
	globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
		const url = typeof input === 'string' ? input : input instanceof URL ? input.href : input.url;

		if (url.includes(SEND)) {
			const body = reqJSON(init);
			return new Response(JSON.stringify({ runId: String(body.idempotencyKey) }), {
				status: 200,
				headers: { 'content-type': 'application/json' }
			});
		}
		if (url.includes(HISTORY)) {
			counts.history++;
			return new Response(JSON.stringify({ messages: [] }), {
				status: 200,
				headers: { 'content-type': 'application/json' }
			});
		}
		if (url.includes(SUBSCRIBE)) {
			const body = new ReadableStream<Uint8Array>({
				start(c) {
					c.enqueue(connectFrame(0, { sessionKey })); // ready, then silence forever
				}
			});
			return new Response(body, {
				status: 200,
				headers: { 'content-type': 'application/connect+json' }
			});
		}
		return realFetch!(input, init);
	}) as typeof globalThis.fetch;

	// Small silence threshold so the watchdog fires fast in-test.
	const store = makeChatStore(sessionKey, { reconcileSilenceMs: 300 });
	store.startSubscribe();
	await store.send('hello');

	// No events ever arrive: after the silence threshold, reconcile fires.
	const deadline = performance.now() + 2000;
	while (counts.history < 1 && performance.now() < deadline) {
		await new Promise((r) => setTimeout(r, 25));
	}
	store.dispose();

	expect(counts.history, 'a hung stream must still trigger a history reconcile').toBeGreaterThanOrEqual(1);
});
