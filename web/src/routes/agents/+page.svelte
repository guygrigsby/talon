<script lang="ts">
	// Agents tab: shows the primary agent (the one the user talks
	// to) plus the fleet of subagents the primary can delegate to
	// via the `subagent` tool. Read-only this pass — editing
	// agent.model / workspace lands later (via ConfigService set
	// or a typed AgentsService.Patch).

	import { loadAgents, type AgentEntry } from '$lib/gateway/agents';

	let primary = $state<AgentEntry | null>(null);
	let fleet = $state<AgentEntry[]>([]);
	let loadError = $state<string | null>(null);
	let loading = $state(true);

	async function load() {
		loading = true;
		loadError = null;
		try {
			const view = await loadAgents();
			const def = view.entries.find((a) => a.id === view.defaultId) ?? view.entries[0] ?? null;
			primary = def;
			fleet = def ? view.entries.filter((a) => a.id !== def.id) : view.entries;
			fleet.sort((a, b) => a.id.localeCompare(b.id));
		} catch (err) {
			loadError = err instanceof Error ? err.message : String(err);
		} finally {
			loading = false;
		}
	}

	$effect(() => {
		load();
	});
</script>

<section class="panel" aria-labelledby="agents-title">
	<header class="head">
		<h1 id="agents-title" class="title">Agents</h1>
		<span class="t-mono dim">primary + fleet</span>
		<button type="button" class="op" onclick={load} disabled={loading}>
			{loading ? 'Loading…' : 'Refresh'}
		</button>
	</header>

	{#if loadError}
		<div class="err t-mono" role="status">{loadError}</div>
	{:else if loading && !primary && fleet.length === 0}
		<div class="t-mono dim">Loading agents…</div>
	{:else}
		<section class="block" aria-labelledby="primary-title">
			<header class="block-head">
				<h2 id="primary-title" class="block-title">Primary</h2>
				<p class="block-sub">
					The agent the chat composer sends to. The primary can delegate to subagents
					(below) by calling the <code class="t-mono">subagent</code> tool.
				</p>
			</header>
			{#if primary}
				<article class="card primary">
					<header class="card-head">
						<span class="card-id t-mono">{primary.id}</span>
						<span class="t-label badge">primary</span>
					</header>
					<dl class="card-body">
						<dt class="t-label">model</dt>
						<dd class="t-mono">{primary.primaryModel || '(unset)'}</dd>
						{#if primary.workspace}
							<dt class="t-label">workspace</dt>
							<dd class="t-mono break">{primary.workspace}</dd>
						{/if}
					</dl>
				</article>
			{:else}
				<div class="t-mono dim">No primary agent configured.</div>
			{/if}
		</section>

		<section class="block" aria-labelledby="fleet-title">
			<header class="block-head">
				<h2 id="fleet-title" class="block-title">Fleet · {fleet.length}</h2>
				<p class="block-sub">
					Subagents the primary can call. Each runs its own model and workspace; tool
					events bubble back into the primary's transcript.
				</p>
			</header>
			{#if fleet.length === 0}
				<div class="t-mono dim">No subagents configured.</div>
			{:else}
				<table class="fleet-table">
					<thead>
						<tr>
							<th scope="col">id</th>
							<th scope="col">model</th>
							<th scope="col">workspace</th>
						</tr>
					</thead>
					<tbody>
						{#each fleet as a (a.id)}
							<tr>
								<td class="t-mono name">{a.id}</td>
								<td class="t-mono">{a.primaryModel || '(unset)'}</td>
								<td class="t-mono break">{a.workspace ?? ''}</td>
							</tr>
						{/each}
					</tbody>
				</table>
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
		font-size: var(--fs-sm);
		color: var(--ink-2);
		max-width: 60ch;
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

	.fleet-table {
		width: 100%;
		border-collapse: collapse;
		font-size: var(--fs-sm);
	}
	.fleet-table th,
	.fleet-table td {
		text-align: left;
		padding: 8px var(--s-3);
		border-bottom: 1px solid var(--border);
		vertical-align: top;
	}
	.fleet-table thead th {
		font-size: var(--fs-xs);
		font-weight: 700;
		color: var(--ink-3);
		text-transform: lowercase;
		letter-spacing: var(--tracking-caps, 0.04em);
		border-bottom: 1px solid var(--border-strong, var(--border));
	}
	.fleet-table tbody tr:last-child td {
		border-bottom: 0;
	}
	.fleet-table .name {
		font-weight: 700;
		color: var(--ink);
		white-space: nowrap;
	}
	.fleet-table .break {
		word-break: break-all;
		color: var(--ink-2);
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
	code {
		font-family: var(--ff-mono);
		background: var(--canvas);
		padding: 0 4px;
		border-radius: 3px;
	}

	@media (max-width: 720px) {
		.panel {
			padding: var(--s-4) var(--s-3);
		}
	}
</style>
