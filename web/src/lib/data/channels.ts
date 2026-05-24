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
};

export const channels: Channel[] = [
	{
		id: 'tg-self',
		source: 'telegram',
		name: 'self',
		peer: '@guygrigsby',
		unread: 0,
		lastActive: '14:02',
		status: 'connected',
	},
	{
		id: 'tg-fam',
		source: 'telegram',
		name: 'family',
		peer: '6 members',
		unread: 3,
		lastActive: '13:58',
		status: 'connected',
	},
	{
		id: 'sig-work',
		source: 'signal',
		name: 'eng-oncall',
		peer: '4 members',
		unread: 12,
		lastActive: '13:41',
		status: 'connected',
	},
	{
		id: 'ims-mom',
		source: 'imessage',
		name: 'mom',
		peer: '+1 555 0118',
		unread: 1,
		lastActive: 'yesterday',
		status: 'connected',
	},
	{
		id: 'slack-talon',
		source: 'slack',
		name: '#talon-dev',
		peer: 'acme',
		unread: 0,
		lastActive: '11:30',
		status: 'connecting',
	},
	{
		id: 'cli-default',
		source: 'cli',
		name: 'terminal',
		peer: 'pid 88421',
		unread: 0,
		lastActive: 'now',
		status: 'connected',
	},
	{
		id: 'web-here',
		source: 'web',
		name: 'this session',
		peer: 'localhost',
		unread: 0,
		lastActive: 'now',
		status: 'connected',
	},
	{
		id: 'wh-mac',
		source: 'whisper',
		name: 'mac mic',
		peer: 'macbook',
		unread: 0,
		lastActive: '09:14',
		status: 'disconnected',
	},
	{
		id: 'irc-libera',
		source: 'irc',
		name: '#emacs',
		peer: 'libera.chat',
		unread: 0,
		lastActive: '06:02',
		status: 'error',
	},
];

export const messages: Record<string, Message[]> = {
	'web-here': [
		{
			id: 'm1',
			channelId: 'web-here',
			role: 'user',
			author: 'guy',
			body: 'pull the last three errors from internal/server and group by handler.',
			ts: '14:02:09',
		},
		{
			id: 'm2',
			channelId: 'web-here',
			role: 'assistant',
			author: 'sonnet-4.6',
			body: 'Three errors in the last 24h, grouped by handler:\n\n1. chat.send → context cancelled before first token (×2)\n2. models.list → upstream openclaw returned 502\n3. health → none\n\nThe chat.send cancellations both came from the same telegram channel — looks like the user closed the chat before the model warmed up. Want me to check whether we should keep streaming on disconnect?',
			ts: '14:02:11',
			model: 'sonnet-4.6',
			tokens: { in: 412, out: 87 },
			latencyMs: 1840,
			toolCalls: [
				{
					name: 'logs.search',
					args: { window: '24h', level: 'error', path: 'internal/server' },
					result: '3 matches',
					durationMs: 142,
				},
				{
					name: 'logs.group_by',
					args: { key: 'handler' },
					result: '3 groups',
					durationMs: 18,
				},
			],
		},
		{
			id: 'm3',
			channelId: 'web-here',
			role: 'user',
			author: 'guy',
			body: 'yeah, draft a beads issue for it. low priority.',
			ts: '14:03:42',
		},
		{
			id: 'm4',
			channelId: 'web-here',
			role: 'assistant',
			author: 'sonnet-4.6',
			body: 'Drafted. Title: "chat.send: continue streaming after client disconnect". Tagged P3, area:server. Want me to open it or just leave the draft?',
			ts: '14:03:44',
			model: 'sonnet-4.6',
			tokens: { in: 528, out: 41 },
			latencyMs: 920,
			toolCalls: [
				{
					name: 'beads.draft',
					args: { title: 'chat.send: continue streaming after client disconnect', priority: 'P3' },
					result: 'draft talon-???',
					durationMs: 67,
				},
			],
		},
	],
};

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
