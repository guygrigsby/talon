// Helpers around ModelsService.List + AuthStatus that materialize
// a "selectable models" list for the composer picker.
//
// Shape: ModelsService.List returns a JSONPayload whose inner JSON
// is `{models:[{provider,id,name,...}]}`. AuthStatus returns
// `{providers:[{provider,status}], ts}`. Selectable = models whose
// provider has status="ok". Anything else gets a "no auth" tag so
// the FE can render it disabled rather than hiding it (useful for
// debugging which provider needs credentials).

import { create } from '@bufbuild/protobuf';
import { ConfigSetRequestSchema } from './gen/talon/v1/config_pb.js';
import { getConfigClient, getModelsClient } from './connect';

export type ModelCost = {
	input?: number;
	output?: number;
	cacheRead?: number;
	cacheWrite?: number;
	source?: string;
};

export type ModelEntry = {
	provider: string;
	id: string;
	name: string;
	api?: string;
	contextWindow?: number;
	maxTokens?: number;
	input?: string[];
	reasoning?: boolean;
	cost?: ModelCost;
	authOk: boolean;
	alias?: string;
};

type RawListed = {
	models?: Array<{
		provider: string;
		id: string;
		name?: string;
		api?: string;
		contextWindow?: number;
		maxTokens?: number;
		input?: string[];
		reasoning?: boolean;
		cost?: ModelCost;
		alias?: string;
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
			api: m.api,
			contextWindow: m.contextWindow,
			maxTokens: m.maxTokens,
			input: m.input,
			reasoning: m.reasoning,
			cost: m.cost,
			authOk: okProviders.has(m.provider),
			alias: m.alias
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

// setModelAlias writes the alias for one model into the talon
// overlay under agents.defaults.models["<provider/id>"].alias. An
// empty alias deletes the entry's alias field (passing an empty
// valueJson which the server interprets as delete).
//
// This is the simplest editable surface on /models: aliases are
// short user-friendly handles for long model IDs (e.g.
// "deepseek" → "deepseek/deepseek-reasoner"). Other model
// edits (hide flags, default-fallback ordering) can land next.
export async function setModelAlias(
	provider: string,
	modelID: string,
	alias: string
): Promise<void> {
	const path = `agents.defaults.models[${JSON.stringify(`${provider}/${modelID}`)}].alias`;
	const valueJson = alias.trim() === '' ? '' : JSON.stringify(alias.trim());
	await getConfigClient().set(
		create(ConfigSetRequestSchema, {
			path,
			valueJson,
			merge: false
		})
	);
}

export async function setMainDefaultModel(modelID: string): Promise<void> {
	await getConfigClient().set(
		create(ConfigSetRequestSchema, {
			path: 'agents.defaults.model.primary',
			valueJson: JSON.stringify(modelID.trim()),
			merge: false
		})
	);
}
