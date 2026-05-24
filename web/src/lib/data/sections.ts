// Top-level admin/control sections. Promoted to primary nav tabs; each also
// has a route at /<key>. Nothing is wired yet — pages are placeholders for
// the RPC behind them.

export type Section = { key: string; label: string; desc: string };

export const sections: Section[] = [
	{ key: 'health', label: 'Health', desc: 'gateway probe, plugin status, latency histogram' },
	{ key: 'channels', label: 'Channels', desc: 'paired sources, peers, last-seen, errors' },
	{ key: 'agents', label: 'Agents', desc: 'identities, models, fallbacks, workspaces' },
	{ key: 'models', label: 'Models', desc: 'available, default, fallbacks, aliases, auth' },
	{ key: 'secrets', label: 'Secrets', desc: 'audit (literal / ref / empty), migrate, reload' },
	{ key: 'config', label: 'Config', desc: 'merged view (~/.openclaw + ~/.talon), set / unset' },
	{ key: 'logs', label: 'Logs', desc: 'streaming gateway log, filter by handler / channel' },
];

export const sectionMap: Record<string, Section> = Object.fromEntries(
	sections.map((s) => [s.key, s]),
);

// Primary nav: chat workspace first, then the control sections.
export const navTabs: Array<{ key: string; label: string; href: string }> = [
	{ key: 'chat', label: 'chat', href: '/' },
	...sections.map((s) => ({ key: s.key, label: s.key, href: `/${s.key}` })),
];
