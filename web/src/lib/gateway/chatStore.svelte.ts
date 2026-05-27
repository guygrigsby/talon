// Live chat store backed by the typed Connect ChatService. One
// store instance per session-key — components mount one, call
// subscribe(), call send() on user input, and read messages /
// status off the runes. The store translates typed ChatEvent
// payloads into the same Message shape the existing UI renders,
// so the components don't change.

import { create } from '@bufbuild/protobuf';
import {
	ChatSendRequestSchema,
	ChatHistoryRequestSchema,
	ChatSubscribeRequestSchema,
	type ChatEvent,
	type HistoryRow
} from './gen/talon/v1/chat_pb.js';
import { SessionsPatchRequestSchema } from './gen/talon/v1/sessions_pb.js';
import { getChatClient, getSessionsClient } from './connect.js';
import type { Message, ToolCall } from '../data/channels.js';

export type ChatStatus = 'idle' | 'loading' | 'streaming' | 'error';

// makeChatStore returns a reactive store for one session-key.
// The Svelte 5 runes inside (`$state`) only work in `.svelte.ts`
// (or `.svelte`) files — that's why this file has the .svelte.ts
// extension.
export function makeChatStore(sessionKey: string) {
	let messages = $state<Message[]>([]);
	let status = $state<ChatStatus>('idle');
	let errorMessage = $state<string | null>(null);
	// Track the currently-streaming assistant message by run_id so
	// delta events update the right bubble; cleared on final.
	let activeRunId = $state<string | null>(null);
	// Per-session model override (null = agent default). Echoes the
	// SessionsService.Patch state the server keeps; updated
	// optimistically on setModel.
	let model = $state<string | null>(null);

	let subscribeAbort: AbortController | null = null;

	async function loadHistory() {
		status = 'loading';
		errorMessage = null;
		try {
			const client = getChatClient();
			const res = await client.history(
				create(ChatHistoryRequestSchema, { sessionKey, limit: 0 })
			);
			messages = res.messages.map(historyRowToMessage).filter((m): m is Message => m != null);
			status = 'idle';
		} catch (err) {
			status = 'error';
			errorMessage = friendlyAuthError(err);
		}
	}

	async function startSubscribe() {
		// Cancel any prior stream — a re-subscribe after a network
		// blip shouldn't leak the old reader.
		subscribeAbort?.abort();
		const abort = new AbortController();
		subscribeAbort = abort;
		const client = getChatClient();
		try {
			for await (const ev of client.subscribe(
				create(ChatSubscribeRequestSchema, { sessionKey }),
				{ signal: abort.signal }
			)) {
				if (abort.signal.aborted) return;
				applyEvent(ev);
			}
		} catch (err) {
			if (abort.signal.aborted) return;
			status = 'error';
			errorMessage = friendlyAuthError(err);
		}
	}

	async function send(text: string) {
		const trimmed = text.trim();
		if (!trimmed) return;
		if (isClearContextCommand(trimmed)) {
			await clearContext(trimmed);
			return;
		}
		const runId = makeRunId();
		// Optimistic user echo so the transcript reflects intent
		// before the server roundtrip. Timestamp is local-clock;
		// the gateway doesn't echo a user-turn event so this stays
		// even after Send resolves.
		const localId = `local-${Date.now()}`;
		messages = [
			...messages,
			{
				id: localId,
				channelId: sessionKey,
				role: 'user',
				author: 'you',
				body: trimmed,
				ts: nowLabel()
			}
		];
		status = 'streaming';
		errorMessage = null;
		activeRunId = runId;
		try {
			const client = getChatClient();
			await client.send(
				create(ChatSendRequestSchema, {
					sessionKey,
					message: trimmed,
					idempotencyKey: runId
				})
			);
			scheduleHistoryReconcile(runId);
		} catch (err) {
			status = 'error';
			errorMessage = friendlyAuthError(err);
			if (activeRunId === runId) activeRunId = null;
		}
	}

	async function clearContext(command: string) {
		status = 'loading';
		errorMessage = null;
		activeRunId = null;
		try {
			const client = getChatClient();
			await client.send(
				create(ChatSendRequestSchema, {
					sessionKey,
					message: command,
					idempotencyKey: makeRunId()
				})
			);
			messages = [];
			status = 'idle';
		} catch (err) {
			status = 'error';
			errorMessage = friendlyAuthError(err);
		}
	}

	function applyEvent(ev: ChatEvent) {
		switch (ev.payload.case) {
			case 'delta': {
				upsertAssistant(ev, ev.payload.value.cumulative, false);
				if (status !== 'streaming') status = 'streaming';
				break;
			}
			case 'thinking': {
				upsertThinking(ev, ev.payload.value.cumulative);
				if (status !== 'streaming') status = 'streaming';
				break;
			}
			case 'final': {
				upsertAssistant(ev, ev.payload.value.text, true);
				if (activeRunId === ev.runId) activeRunId = null;
				status = 'idle';
				break;
			}
			case 'aborted': {
				upsertAssistant(ev, ev.payload.value.text, true);
				if (activeRunId === ev.runId) activeRunId = null;
				status = 'idle';
				break;
			}
			case 'error': {
				status = 'error';
				errorMessage = ev.payload.value.message || ev.payload.value.kind || 'chat error';
				if (activeRunId === ev.runId) activeRunId = null;
				break;
			}
			case 'toolStart': {
				appendToolCall(ev, {
					name: ev.payload.value.name,
					args: parseJSON(ev.payload.value.argsJson) ?? {}
				});
				break;
			}
			case 'toolResult': {
				updateToolCallResult(ev, ev.payload.value.name, ev.payload.value.output);
				break;
			}
		}
	}

	function scheduleHistoryReconcile(runId: string) {
		setTimeout(() => {
			if (activeRunId !== runId || status !== 'streaming') return;
			loadHistory();
		}, 1800);
	}

	function upsertAssistant(ev: ChatEvent, text: string, final: boolean) {
		const id = `run-${ev.runId}`;
		const existing = messages.find((m) => m.id === id);
		if (existing) {
			existing.body = text;
			if (final) existing.ts = nowLabel();
			return;
		}
		messages = [
			...messages,
			{
				id,
				channelId: ev.sessionKey || sessionKey,
				role: 'assistant',
				author: 'assistant',
				body: text,
				ts: nowLabel()
			}
		];
	}

	function upsertThinking(ev: ChatEvent, thinking: string) {
		const id = `run-${ev.runId}`;
		const existing = messages.find((m) => m.id === id);
		if (existing) {
			existing.thinking = thinking;
			return;
		}
		// Thinking can land before any visible delta (the model
		// reasons first, then speaks). Seed an empty-body
		// assistant bubble so subsequent deltas append into it.
		messages = [
			...messages,
			{
				id,
				channelId: ev.sessionKey || sessionKey,
				role: 'assistant',
				author: 'assistant',
				body: '',
				ts: nowLabel(),
				thinking
			}
		];
	}

	function appendToolCall(ev: ChatEvent, call: ToolCall) {
		const id = `run-${ev.runId}`;
		let target = messages.find((m) => m.id === id);
		if (!target) {
			// Tool can start before the first delta when the model
			// goes straight to a tool call. Seed an empty assistant
			// bubble that later deltas/final will fill in.
			messages = [
				...messages,
				{
					id,
					channelId: ev.sessionKey || sessionKey,
					role: 'assistant',
					author: 'assistant',
					body: '',
					ts: nowLabel(),
					toolCalls: [call]
				}
			];
			return;
		}
		target.toolCalls = [...(target.toolCalls ?? []), call];
	}

	function updateToolCallResult(ev: ChatEvent, name: string, output: string) {
		const target = messages.find((m) => m.id === `run-${ev.runId}`);
		if (!target || !target.toolCalls) return;
		// Match the most recent tool call with the same name that
		// doesn't yet have a result — keeps multi-call results in
		// the right slots without needing tool_call_id plumbing
		// through the Message shape.
		for (let i = target.toolCalls.length - 1; i >= 0; i--) {
			if (target.toolCalls[i].name === name && target.toolCalls[i].result === undefined) {
				target.toolCalls[i] = { ...target.toolCalls[i], result: output };
				return;
			}
		}
	}

	function dispose() {
		subscribeAbort?.abort();
		subscribeAbort = null;
	}

	// setModel persists a per-session model override on the gateway
	// via SessionsService.Patch. The next chat.send for this session
	// reads the override (sessions.Model on the server side) and
	// uses it instead of the agent's primary model. Empty string
	// reverts to the agent default. Optimistic local update so the
	// composer reflects the new choice immediately; on patch failure
	// the local state rolls back and surfaces the error.
	async function setModel(next: string) {
		const prev = model;
		model = next || null;
		try {
			await getSessionsClient().patch(
				create(SessionsPatchRequestSchema, {
					sessionKey,
					patchJson: JSON.stringify({ model: next })
				})
			);
		} catch (err) {
			model = prev;
			errorMessage = err instanceof Error ? err.message : String(err);
		}
	}

	return {
		get messages() {
			return messages;
		},
		get status() {
			return status;
		},
		get errorMessage() {
			return errorMessage;
		},
		get activeRunId() {
			return activeRunId;
		},
		get model() {
			return model;
		},
		loadHistory,
		startSubscribe,
		send,
		setModel,
		dispose
	};
}

// historyRowToMessage maps the typed proto HistoryRow oneof onto
// the Message shape the UI components consume. Unsupported
// variants return null so the caller can filter them out.
function historyRowToMessage(row: HistoryRow): Message | null {
	const id = row.id || `row-${row.seq}`;
	switch (row.body.case) {
		case 'user':
			return {
				id,
				channelId: '',
				role: 'user',
				author: 'you',
				body: row.body.value.text,
				ts: ''
			};
		case 'assistant':
			return {
				id,
				channelId: '',
				role: 'assistant',
				author: 'assistant',
				body: row.body.value.text,
				ts: '',
				toolCalls: row.body.value.toolUses.map((tu) => ({
					name: tu.name,
					args: parseJSON(tu.argsJson) ?? {}
				}))
			};
		case 'toolUse':
			return {
				id,
				channelId: '',
				role: 'assistant',
				author: 'assistant',
				body: '',
				ts: '',
				toolCalls: [
					{ name: row.body.value.name, args: parseJSON(row.body.value.argsJson) ?? {} }
				]
			};
		case 'toolResult':
			// Tool-result rows fold into the prior assistant row's
			// toolCalls in the live stream, but coming from History
			// they arrive as standalone rows. Render as a small
			// assistant turn so the user sees the output; rich
			// pairing can come later.
			return {
				id,
				channelId: '',
				role: 'assistant',
				author: `tool:${row.body.value.name}`,
				body: row.body.value.output,
				ts: ''
			};
		default:
			return null;
	}
}

function parseJSON(raw: string): Record<string, unknown> | null {
	if (!raw) return null;
	try {
		const parsed = JSON.parse(raw);
		return typeof parsed === 'object' && parsed !== null
			? (parsed as Record<string, unknown>)
			: null;
	} catch {
		return null;
	}
}

function isClearContextCommand(text: string): boolean {
	return ['/clear', '/clear-context', '/context clear'].includes(text.trim().toLowerCase());
}

// friendlyAuthError rewrites the bare Connect "unauthenticated"
// surface into actionable guidance. Token auth is the only
// scenario where this fires today — the gateway accepted the
// request but rejected the Authorization header (or its absence).
// Pointing users at the `talon dashboard` command resolves the
// most-common case (browser opened directly without the token
// fragment).
function friendlyAuthError(err: unknown): string {
	const raw = err instanceof Error ? err.message : String(err);
	if (/unauthenticated|invalid or missing auth token/i.test(raw)) {
		return 'Gateway requires a token. Run `talon dashboard` to open the UI with auto-auth, or append #token=<your-token> to the URL.';
	}
	return raw;
}

function nowLabel(): string {
	const d = new Date();
	const hh = String(d.getHours()).padStart(2, '0');
	const mm = String(d.getMinutes()).padStart(2, '0');
	const ss = String(d.getSeconds()).padStart(2, '0');
	return `${hh}:${mm}:${ss}`;
}

function makeRunId(): string {
	if (typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function') {
		return crypto.randomUUID();
	}
	return `run-${Date.now()}-${Math.random().toString(16).slice(2)}`;
}
