// Helpers around ModelsService.List + AuthStatus that materialize
// a "selectable models" list for the composer picker.
//
// Shape: ModelsService.List returns a JSONPayload whose inner JSON
// is `{models:[{provider,id,name,...}]}`. AuthStatus returns
// `{providers:[{provider,status}], ts}`. Selectable = models whose
// provider has status="ok". Anything else gets a "no auth" tag so
// the FE can render it disabled rather than hiding it (useful for
// debugging which provider needs credentials).

import { getModelsClient } from './connect';

export type ModelEntry = {
	provider: string;
	id: string;
	name: string;
	contextWindow?: number;
	reasoning?: boolean;
	authOk: boolean;
};

type RawListed = {
	models?: Array<{
		provider: string;
		id: string;
		name?: string;
		contextWindow?: number;
		reasoning?: boolean;
	}>;
};

type RawAuthStatus = {
	providers?: Array<{ provider: string; status: string }>;
};

export async function loadSelectableModels(): Promise<ModelEntry[]> {
	const client = getModelsClient();
	const [listRes, authRes] = await Promise.all([client.list({}), client.authStatus({})]);
	const list = JSON.parse(listRes.json) as RawListed;
	const auth = JSON.parse(authRes.json) as RawAuthStatus;

	const okProviders = new Set<string>();
	for (const p of auth.providers ?? []) {
		if (p.status === 'ok') okProviders.add(p.provider);
	}

	const out: ModelEntry[] = [];
	for (const m of list.models ?? []) {
		out.push({
			provider: m.provider,
			id: m.id,
			name: m.name ?? m.id,
			contextWindow: m.contextWindow,
			reasoning: m.reasoning,
			authOk: okProviders.has(m.provider)
		});
	}
	// Stable sort: authed-providers first, then alphabetical by
	// provider + name. The picker renders this verbatim so the
	// usable models cluster at the top.
	out.sort((a, b) => {
		if (a.authOk !== b.authOk) return a.authOk ? -1 : 1;
		if (a.provider !== b.provider) return a.provider.localeCompare(b.provider);
		return a.name.localeCompare(b.name);
	});
	return out;
}

// Canonical wire form for a model: `<provider>/<id>` — matches
// what chat.send + sessions.patch consume on the server.
export function modelKey(m: Pick<ModelEntry, 'provider' | 'id'>): string {
	return `${m.provider}/${m.id}`;
}
