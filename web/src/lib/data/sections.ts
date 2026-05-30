// Top-level admin/control sections. Keep only sections that deserve their own
// workspace. Runtime status and channel/config hints live in the chat chrome.

export type Section = { key: string; label: string; desc: string };

export const sections: Section[] = [
	{ key: 'agents', label: 'Agents', desc: 'main agent, subagents, models' },
	{ key: 'models', label: 'Models', desc: 'available, default, fallbacks, aliases, auth' },
	// No secrets section: secret values live in keychain / 1Password behind
	// op:// and keychain:// refs (ADR 0006) and are never exposed in the UI.
	// Plaintext detection / migrate, if needed, belongs in a `talon secrets`
	// CLI check, not a web surface.
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
