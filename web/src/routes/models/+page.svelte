<script lang="ts">
	// Models tab: lists every model talon knows about, grouped by
	// provider, with the per-provider auth status surfaced next to
	// the header. Reuses loadSelectableModels which already fans
	// ModelsService.List + AuthStatus together.

	import { loadSelectableModels, type ModelEntry } from '$lib/gateway/models';

	let models = $state<ModelEntry[]>([]);
	let loadError = $state<string | null>(null);
	let loading = $state(true);

	async function load() {
		loading = true;
		loadError = null;
		try {
			models = await loadSelectableModels();
		} catch (err) {
			loadError = err instanceof Error ? err.message : String(err);
		} finally {
			loading = false;
		}
	}

	$effect(() => {
		load();
	});

	type Group = {
		provider: string;
		authOk: boolean;
		entries: ModelEntry[];
	};

	const groups = $derived.by<Group[]>(() => {
		const map = new Map<string, Group>();
		for (const m of models) {
			let g = map.get(m.provider);
			if (!g) {
				g = { provider: m.provider, authOk: m.authOk, entries: [] };
				map.set(m.provider, g);
			}
			g.entries.push(m);
		}
		return [...map.values()];
	});

	const totalCount = $derived(models.length);
	const reasoningCount = $derived(models.filter((m) => m.reasoning).length);

	function ctxLabel(window?: number): string {
		if (!window) return '';
		if (window >= 1_000_000) return `${(window / 1_000_000).toFixed(window % 1_000_000 === 0 ? 0 : 1)}M`;
		if (window >= 1_000) return `${(window / 1_000).toFixed(window % 1_000 === 0 ? 0 : 1)}K`;
		return String(window);
	}
</script>

<section class="panel" aria-labelledby="models-title">
	<header class="head">
		<h1 id="models-title" class="title">Models</h1>
		<span class="t-mono dim">
			{totalCount} model{totalCount === 1 ? '' : 's'} · {reasoningCount} reasoning
		</span>
		<button type="button" class="op" onclick={load} disabled={loading}>
			{loading ? 'Loading…' : 'Refresh'}
		</button>
	</header>

	{#if loadError}
		<div class="err t-mono" role="status">{loadError}</div>
	{:else if loading && totalCount === 0}
		<div class="t-mono dim">Loading models…</div>
	{:else if totalCount === 0}
		<div class="t-mono dim">No models registered.</div>
	{:else}
		{#each groups as g (g.provider)}
			<section class="block" aria-labelledby="provider-{g.provider}">
				<header class="block-head">
					<h2 id="provider-{g.provider}" class="block-title">
						{g.provider}
						<span class="t-label badge {g.authOk ? 'ok' : 'bad'}">
							{g.authOk ? 'auth ok' : 'no auth'}
						</span>
					</h2>
					<p class="block-sub">
						{g.entries.length} model{g.entries.length === 1 ? '' : 's'} configured under
						<code class="t-mono">{g.provider}/</code>.
					</p>
				</header>
				<table class="models-table">
					<thead>
						<tr>
							<th scope="col">id</th>
							<th scope="col">name</th>
							<th scope="col" class="num">ctx</th>
							<th scope="col">reasoning</th>
						</tr>
					</thead>
					<tbody>
						{#each g.entries as m (m.id)}
							<tr class={!m.authOk ? 'unauthed' : ''}>
								<td class="t-mono id">{m.provider}/{m.id}</td>
								<td>{m.name}</td>
								<td class="t-num num">{ctxLabel(m.contextWindow)}</td>
								<td>{m.reasoning ? 'yes' : ''}</td>
							</tr>
						{/each}
					</tbody>
				</table>
			</section>
		{/each}
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
		display: flex;
		align-items: baseline;
		gap: var(--s-2);
	}
	.block-sub {
		margin: 0;
		font-size: var(--fs-sm);
		color: var(--ink-2);
		max-width: 60ch;
	}

	.badge {
		font-size: var(--fs-xs);
		padding: 1px 6px;
		border-radius: 999px;
		border: 1px solid var(--border);
	}
	.badge.ok {
		color: var(--good, var(--accent));
		border-color: var(--good, var(--accent));
	}
	.badge.bad {
		color: var(--accent-strong, var(--ink));
		border-color: var(--accent-strong, var(--border));
	}

	.models-table {
		width: 100%;
		border-collapse: collapse;
		font-size: var(--fs-sm);
	}
	.models-table th,
	.models-table td {
		text-align: left;
		padding: 6px var(--s-3);
		border-bottom: 1px solid var(--border);
		vertical-align: top;
	}
	.models-table thead th {
		font-size: var(--fs-xs);
		font-weight: 700;
		color: var(--ink-3);
		text-transform: lowercase;
		letter-spacing: var(--tracking-caps, 0.04em);
		border-bottom: 1px solid var(--border-strong, var(--border));
	}
	.models-table tbody tr:last-child td {
		border-bottom: 0;
	}
	.models-table .id {
		color: var(--ink);
		font-weight: 700;
		white-space: nowrap;
	}
	.models-table .num {
		text-align: right;
		font-variant-numeric: tabular-nums;
		color: var(--ink-2);
	}
	.models-table tr.unauthed td {
		color: var(--ink-3);
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
