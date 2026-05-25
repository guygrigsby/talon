// Typed helpers around AgentsService.List. Mirrors the
// ModelEntry helper in models.ts — flattens the JSONPayload
// into a useful shape with the picker fields the FE needs.

import { getAgentsClient } from './connect';

export type AgentEntry = {
	id: string;
	name: string;
	primaryModel: string;
	primaryModelName: string;
	workspace?: string;
};

export type AgentsView = {
	entries: AgentEntry[];
	defaultId: string;
};

type RawAgent = {
	id: string;
	name?: string;
	workspace?: string;
	model?: {
		primary?: string;
		primaryName?: string;
	};
};

type RawList = {
	agents?: RawAgent[];
	defaultId?: string;
};

export async function loadAgents(): Promise<AgentsView> {
	const client = getAgentsClient();
	const res = await client.list({});
	const parsed = JSON.parse(res.json) as RawList;
	const entries: AgentEntry[] = [];
	for (const a of parsed.agents ?? []) {
		entries.push({
			id: a.id,
			name: a.name ?? a.id,
			primaryModel: a.model?.primary ?? '',
			primaryModelName: a.model?.primaryName ?? a.model?.primary ?? '',
			workspace: a.workspace
		});
	}
	return {
		entries,
		defaultId: parsed.defaultId ?? entries[0]?.id ?? ''
	};
}
