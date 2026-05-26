// Typed helpers around AgentsService.List. Mirrors the
// ModelEntry helper in models.ts — flattens the JSONPayload
// into a useful shape with the picker fields the FE needs.

import { getAgentsClient } from './connect';

export type AgentEntry = {
	id: string;
	name: string;
	kind: 'main' | 'subagent' | string;
	primaryModel: string;
	primaryModelName: string;
	modelSource?: string;
	workspace?: string;
	description?: string;
	source?: string;
	path?: string;
	tools?: string[];
	promptChars?: number;
};

export type AgentsView = {
	entries: AgentEntry[];
	defaultId: string;
	subagentsDir?: string;
};

type RawAgent = {
	id: string;
	name?: string;
	kind?: string;
	workspace?: string;
	description?: string;
	source?: string;
	path?: string;
	tools?: string[];
	model?: {
		primary?: string;
		primaryName?: string;
		source?: string;
	};
	promptChars?: number;
};

type RawList = {
	agents?: RawAgent[];
	defaultId?: string;
	subagentsDir?: string;
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
			kind: a.kind ?? 'main',
			primaryModel: a.model?.primary ?? '',
			primaryModelName: a.model?.primaryName ?? a.model?.primary ?? '',
			modelSource: a.model?.source,
			workspace: a.workspace,
			description: a.description,
			source: a.source,
			path: a.path,
			tools: a.tools,
			promptChars: a.promptChars
		});
	}
	return {
		entries,
		defaultId: parsed.defaultId ?? entries[0]?.id ?? '',
		subagentsDir: parsed.subagentsDir
	};
}
