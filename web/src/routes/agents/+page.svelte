<script lang="ts">
	import { loadAgents, type AgentEntry } from '$lib/gateway/agents';

	let primary = $state<AgentEntry | null>(null);
	let subagents = $state<AgentEntry[]>([]);
	let subagentsDir = $state('');
	let loadError = $state<string | null>(null);
	let loading = $state(true);

	async function load() {
		loading = true;
		loadError = null;
		try {
			const view = await loadAgents();
			const def =
				view.entries.find((a) => a.id === view.defaultId) ??
				view.entries.find((a) => a.kind !== 'subagent') ??
				view.entries[0] ??
				null;
			primary = def;
			subagents = view.entries.filter((a) => a.kind === 'subagent');
			subagents.sort((a, b) => a.id.localeCompare(b.id));
			subagentsDir = view.subagentsDir ?? '';
		} catch (err) {
			loadError = err instanceof Error ? err.message : String(err);
		} finally {
			loading = false;
		}
	}

	$effect(() => {
		load();
	});

	function modelSourceLabel(source?: string): string {
		switch (source) {
			case 'agent':
				return 'configured';
			case 'frontmatter':
				return 'frontmatter';
			case 'default':
				return 'default';
			default:
				return source || 'default';
		}
	}

	function promptLabel(chars?: number): string {
		if (!chars) return '';
		if (chars >= 1_000) return `${(chars / 1_000).toFixed(chars % 1_000 === 0 ? 0 : 1)}K`;
		return String(chars);
	}

	function toolsLabel(tools?: string[]): string {
		if (!tools) return 'default';
		if (tools.length === 0) return 'none';
		return tools.join(', ');
	}
</script>

<section class="panel" aria-labelledby="agents-title">
	<header class="head">
		<h1 id="agents-title" class="title">Agents</h1>
		<span class="t-mono dim">1 main · {subagents.length} subagent{subagents.length === 1 ? '' : 's'}</span>
		<button type="button" class="op" onclick={load} disabled={loading}>
			{loading ? 'Loading…' : 'Refresh'}
		</button>
	</header>

	{#if loadError}
		<div class="err t-mono" role="status">{loadError}</div>
	{:else if loading && !primary && subagents.length === 0}
		<div class="t-mono dim">Loading agents…</div>
	{:else}
		<section class="block" aria-labelledby="primary-title">
			<header class="block-head">
				<h2 id="primary-title" class="block-title">Main</h2>
			</header>
			{#if primary}
				<article class="card primary">
					<header class="card-head">
						<span class="card-id t-mono">{primary.id}</span>
						<span class="t-label badge">main</span>
					</header>
					<dl class="card-body">
						<dt class="t-label">name</dt>
						<dd>{primary.name}</dd>
						<dt class="t-label">model</dt>
						<dd class="t-mono">{primary.primaryModel || '(unset)'}</dd>
						<dt class="t-label">source</dt>
						<dd>{modelSourceLabel(primary.modelSource)}</dd>
						{#if primary.workspace}
							<dt class="t-label">workspace</dt>
							<dd class="t-mono break">{primary.workspace}</dd>
						{/if}
						<dt class="t-label">tools</dt>
						<dd class="t-mono break">{toolsLabel(primary.tools)}</dd>
					</dl>
				</article>
			{:else}
				<div class="t-mono dim">No main agent configured.</div>
			{/if}
		</section>

		<section class="block" aria-labelledby="subagents-title">
			<header class="block-head">
				<h2 id="subagents-title" class="block-title">Subagents · {subagents.length}</h2>
				{#if subagentsDir}
					<p class="block-sub t-mono">{subagentsDir}</p>
				{/if}
			</header>
			{#if subagents.length === 0}
				<div class="t-mono dim">No subagent files found{#if subagentsDir} in {subagentsDir}{/if}.</div>
			{:else}
				<div class="table-wrap">
					<table class="agents-table">
						<thead>
							<tr>
								<th scope="col">id</th>
								<th scope="col">use when</th>
								<th scope="col">model</th>
								<th scope="col">source</th>
								<th scope="col">tools</th>
								<th scope="col" class="num">prompt</th>
								<th scope="col">file</th>
							</tr>
						</thead>
						<tbody>
							{#each subagents as a (a.id)}
								<tr>
									<td>
										<div class="t-mono name">{a.id}</div>
										{#if a.name && a.name !== a.id}
											<div class="small dim">{a.name}</div>
										{/if}
									</td>
									<td class="purpose">{a.description || ''}</td>
									<td class="model-cell">
										<div class="t-mono">{a.primaryModel || '(unset)'}</div>
										{#if a.primaryModelName && a.primaryModelName !== a.primaryModel}
											<div class="small dim">{a.primaryModelName}</div>
										{/if}
									</td>
									<td>{modelSourceLabel(a.modelSource)}</td>
									<td class="t-mono tools">{toolsLabel(a.tools)}</td>
									<td class="t-num num">{promptLabel(a.promptChars)}</td>
									<td class="t-mono break">{a.path ?? ''}</td>
								</tr>
							{/each}
						</tbody>
					</table>
				</div>
			{/if}
		</section>
	{/if}
</section>

<style>
	.panel {
		flex: 1;
		min-height: 0;
		overflow-y: auto;
		padding: var(--s-6) var(--s-8);
		background: var(--surface);
		color: var(--ink);
		display: flex;
		flex-direction: column;
		gap: var(--s-6);
	}
	.head {
		display: flex;
		align-items: baseline;
		gap: var(--s-3);
	}
	.title {
		font-size: var(--fs-lg);
		font-weight: 700;
	}
	.dim {
		color: var(--ink-3);
	}
	.op {
		margin-left: auto;
		background: var(--canvas);
		border: 1px solid var(--border);
		border-radius: var(--radius);
		padding: 4px var(--s-3);
		font-size: var(--fs-xs);
		color: var(--ink);
		cursor: pointer;
		min-height: var(--tap, 32px);
	}
	.op:disabled {
		opacity: 0.6;
		cursor: not-allowed;
	}
	.op:focus-visible {
		outline: 2px solid var(--accent);
		outline-offset: 2px;
	}
	.err {
		color: var(--accent-strong, var(--ink));
		font-size: var(--fs-sm);
	}

	.block {
		display: flex;
		flex-direction: column;
		gap: var(--s-3);
	}
	.block-head {
		display: flex;
		flex-direction: column;
		gap: 4px;
	}
	.block-title {
		font-size: var(--fs-md);
		font-weight: 700;
	}
	.block-sub {
		margin: 0;
		font-size: var(--fs-xs);
		color: var(--ink-3);
		word-break: break-all;
	}
	.card {
		border: 1px solid var(--border);
		border-radius: var(--radius);
		padding: var(--s-3) var(--s-4);
		background: var(--canvas);
		display: flex;
		flex-direction: column;
		gap: var(--s-2);
	}
	.card.primary {
		border-color: var(--accent);
		border-width: 1px;
		max-width: 480px;
	}
	.table-wrap {
		overflow-x: auto;
	}

	.agents-table {
		width: 100%;
		border-collapse: collapse;
		font-size: var(--fs-sm);
	}
	.agents-table th,
	.agents-table td {
		text-align: left;
		padding: 8px var(--s-3);
		border-bottom: 1px solid var(--border);
		vertical-align: top;
	}
	.agents-table thead th {
		font-size: var(--fs-xs);
		font-weight: 700;
		color: var(--ink-3);
		text-transform: lowercase;
		letter-spacing: var(--tracking-caps, 0.04em);
		border-bottom: 1px solid var(--border-strong, var(--border));
	}
	.agents-table tbody tr:last-child td {
		border-bottom: 0;
	}
	.agents-table .name {
		font-weight: 700;
		color: var(--ink);
		white-space: nowrap;
	}
	.agents-table .purpose {
		color: var(--ink-2);
		max-width: 34ch;
	}
	.agents-table .model-cell {
		min-width: 18ch;
	}
	.agents-table .tools {
		color: var(--ink-2);
		max-width: 24ch;
	}
	.agents-table .break {
		word-break: break-all;
		color: var(--ink-2);
	}
	.agents-table .num {
		text-align: right;
		font-variant-numeric: tabular-nums;
		color: var(--ink-2);
		white-space: nowrap;
	}
	.small {
		font-size: var(--fs-xs);
	}
	.card-head {
		display: flex;
		align-items: baseline;
		gap: var(--s-2);
	}
	.card-id {
		font-weight: 700;
		color: var(--ink);
		font-size: var(--fs-md);
	}
	.badge {
		font-size: var(--fs-xs);
		padding: 1px 6px;
		border: 1px solid var(--accent);
		border-radius: 999px;
		color: var(--accent);
	}
	.card-body {
		display: grid;
		grid-template-columns: max-content 1fr;
		gap: 2px var(--s-3);
		margin: 0;
		font-size: var(--fs-sm);
	}
	.card-body dt {
		color: var(--ink-3);
	}
	.card-body dd {
		margin: 0;
		color: var(--ink);
	}
	.card-body .break {
		word-break: break-all;
	}
	@media (max-width: 720px) {
		.panel {
			padding: var(--s-4) var(--s-3);
		}
	}
</style>
