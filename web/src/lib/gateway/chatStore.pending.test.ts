import { afterEach, expect, test } from 'vitest';
import { makeChatStore } from './chatStore.svelte';

// Regression test for the post-Enter dead air: send() optimistically echoes
// the user's message but used to render nothing for the assistant until the
// first delta/thinking event arrived (model TTFB, worse with reasoning). The
// assistant bubble — with a loading state — should appear the instant the
// user sends, so the UI acknowledges the turn immediately.

const SEND = '/talon.v1.ChatService/Send';

let realFetch: typeof globalThis.fetch | undefined;
afterEach(() => {
	if (realFetch) globalThis.fetch = realFetch;
	realFetch = undefined;
});

test('renders a pending assistant bubble immediately on send, before any event', () => {
	realFetch = globalThis.fetch;
	globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
		const url = typeof input === 'string' ? input : input instanceof URL ? input.href : input.url;
		if (url.includes(SEND)) {
			// Accept the turn but never stream anything back, so the only
			// assistant bubble that can exist is the optimistic placeholder.
			return new Response(JSON.stringify({ runId: 'test-run' }), {
				status: 200,
				headers: { 'content-type': 'application/json' }
			});
		}
		return realFetch!(input, init);
	}) as typeof globalThis.fetch;

	const store = makeChatStore('agent:test:pending');
	// Intentionally not awaited: the optimistic state is added synchronously
	// before send() suspends on the network call.
	void store.send('hello');

	const assistant = store.messages.find((m) => m.role === 'assistant');
	expect(assistant, 'an assistant bubble should appear immediately on send').toBeTruthy();
	expect(assistant?.pending, 'the bubble should be in a pending/loading state').toBe(true);
	expect(assistant?.body, 'the placeholder should have no body yet').toBe('');

	const user = store.messages.find((m) => m.role === 'user');
	expect(user?.body, 'the user echo should also be present').toBe('hello');
});
