// Mock channel + transcript data. Wired to the real gateway later via
// src/lib/gateway/client.ts. Keep this realistic enough that layout work
// against it surfaces real cases (long names, unicode, big numbers).

export type Source =
	| 'telegram'
	| 'signal'
	| 'imessage'
	| 'slack'
	| 'discord'
	| 'whisper'
	| 'cli'
	| 'web'
	| 'irc';

export type ChannelStatus = 'connected' | 'connecting' | 'disconnected' | 'error';

export type Channel = {
	id: string;
	source: Source;
	name: string;
	peer?: string;
	unread: number;
	lastActive: string;
	status: ChannelStatus;
};

export type ToolCall = {
	name: string;
	args: Record<string, unknown>;
	result?: string;
	durationMs?: number;
};

export type Message = {
	id: string;
	channelId: string;
	role: 'user' | 'assistant' | 'system';
	author: string;
	body: string;
	ts: string;
	tokens?: { in: number; out: number };
	model?: string;
	latencyMs?: number;
	toolCalls?: ToolCall[];
	// Hidden reasoning trace (DeepSeek Reasoner, o-series, Claude
	// thinking). When set, the assistant bubble renders a
	// collapsible "thinking" block above the visible body.
	thinking?: string;
};

// Only the live web session ships today. The other source bridges
// (telegram, signal, imessage, slack, discord, whisper, cli, irc)
// are real concepts in the gateway but the FE wire for each is
// still TBD — adding mock rows here misled the layout into looking
// "populated" with channels that don't go anywhere. Real channels
// will get appended once their bridges land.
export const channels: Channel[] = [
	{
		id: 'web-here',
		source: 'web',
		name: 'this session',
		peer: 'localhost',
		unread: 0,
		lastActive: 'now',
		status: 'connected',
	},
];

// Mock transcripts removed — the only channel that exists today
// is web-here, and it pulls real history + events from the
// gateway. Non-live channels render an empty stream and the
// composer stays disabled (see Transcript.svelte's `wired` flag).
export const messages: Record<string, Message[]> = {};

export const sourceOrder: Source[] = [
	'web',
	'cli',
	'telegram',
	'signal',
	'imessage',
	'slack',
	'discord',
	'whisper',
	'irc',
];

export const sourceLabel: Record<Source, string> = {
	telegram: 'TELEGRAM',
	signal: 'SIGNAL',
	imessage: 'IMESSAGE',
	slack: 'SLACK',
	discord: 'DISCORD',
	whisper: 'WHISPER',
	cli: 'CLI',
	web: 'WEB',
	irc: 'IRC',
};

export function bySource(list: Channel[]): Map<Source, Channel[]> {
	const m = new Map<Source, Channel[]>();
	for (const s of sourceOrder) m.set(s, []);
	for (const c of list) m.get(c.source)?.push(c);
	for (const [k, v] of m) if (v.length === 0) m.delete(k);
	return m;
}
