<script lang="ts">
	// Models tab: lists every model talon knows about, grouped by
	// provider, with the per-provider auth status surfaced next to
	// the header. Reuses loadSelectableModels which already fans
	// ModelsService.List + AuthStatus together.

	import { loadAgents } from '$lib/gateway/agents';
	import {
		loadSelectableModels,
		setMainDefaultModel,
		setModelAlias,
		modelKey,
		type ModelEntry
	} from '$lib/gateway/models';

	let models = $state<ModelEntry[]>([]);
	let query = $state('');
	let loadError = $state<string | null>(null);
	let loading = $state(true);
	// Per-row alias draft state. Keyed by `provider/id` so a row's
	// edits survive a re-render that swaps the model array.
	let aliasDrafts = $state<Record<string, string>>({});
	let aliasSaving = $state<Record<string, boolean>>({});
	let aliasError = $state<Record<string, string>>({});
	let defaultModel = $state('');
	let defaultSaving = $state<Record<string, boolean>>({});
	let defaultError = $state<string | null>(null);

	// Debounce window for autosave. Long enough to coalesce typing,
	// short enough that focus-and-tab-away feels immediate.
	const SAVE_DEBOUNCE_MS = 500;
	const saveTimers = new Map<string, ReturnType<typeof setTimeout>>();

	async function load() {
		loading = true;
		loadError = null;
		try {
			const [nextModels, agents] = await Promise.all([loadSelectableModels(), loadAgents()]);
			models = nextModels;
			const main = agents.entries.find((a) => a.id === agents.defaultId) ?? agents.entries[0];
			defaultModel = main?.primaryModel ?? '';
			aliasDrafts = Object.fromEntries(models.map((m) => [modelKey(m), m.alias ?? '']));
			aliasError = {};
			defaultError = null;
		} catch (err) {
			loadError = err instanceof Error ? err.message : String(err);
		} finally {
			loading = false;
		}
	}

	async function saveAlias(m: ModelEntry) {
		const key = modelKey(m);
		const next = (aliasDrafts[key] ?? '').trim();
		const current = (m.alias ?? '').trim();
		if (next === current) return;
		aliasSaving[key] = true;
		delete aliasError[key];
		aliasError = { ...aliasError };
		try {
			await setModelAlias(m.provider, m.id, next);
			m.alias = next || undefined;
		} catch (err) {
			aliasError = { ...aliasError, [key]: err instanceof Error ? err.message : String(err) };
		} finally {
			aliasSaving = { ...aliasSaving, [key]: false };
		}
	}

	function scheduleSave(m: ModelEntry) {
		const key = modelKey(m);
		const existing = saveTimers.get(key);
		if (existing) clearTimeout(existing);
		saveTimers.set(
			key,
			setTimeout(() => {
				saveTimers.delete(key);
				saveAlias(m);
			}, SAVE_DEBOUNCE_MS)
		);
	}

	function flushSave(m: ModelEntry) {
		const key = modelKey(m);
		const existing = saveTimers.get(key);
		if (existing) {
			clearTimeout(existing);
			saveTimers.delete(key);
		}
		saveAlias(m);
	}

	async function makeDefault(m: ModelEntry) {
		const key = modelKey(m);
		if (key === defaultModel) return;
		defaultSaving = { ...defaultSaving, [key]: true };
		defaultError = null;
		try {
			await setMainDefaultModel(key);
			defaultModel = key;
		} catch (err) {
			defaultError = err instanceof Error ? err.message : String(err);
		} finally {
			defaultSaving = { ...defaultSaving, [key]: false };
		}
	}

	$effect(() => {
		load();
	});

	$effect(() => {
		return () => {
			for (const id of saveTimers.values()) clearTimeout(id);
			saveTimers.clear();
		};
	});

	type Group = {
		provider: string;
		authOk: boolean;
		entries: ModelEntry[];
	};

	const filteredModels = $derived.by<ModelEntry[]>(() => {
		const q = query.trim().toLowerCase();
		if (!q) return models;
		return models.filter((m) => matchesModel(m, q));
	});

	const groups = $derived.by<Group[]>(() => {
		const map = new Map<string, Group>();
		for (const m of filteredModels) {
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
	const shownCount = $derived(filteredModels.length);
	const reasoningCount = $derived(models.filter((m) => m.reasoning).length);
	const pricedCount = $derived(models.filter((m) => m.cost?.input != null || m.cost?.output != null).length);
	const defaultEntry = $derived(models.find((m) => modelKey(m) === defaultModel) ?? null);
	const defaultLabel = $derived.by(() => {
		if (!defaultModel) return 'not set';
		if (!defaultEntry) return defaultModel;
		if (defaultEntry.name && defaultEntry.name !== defaultEntry.id) {
			return `${defaultEntry.name} · ${defaultModel}`;
		}
		return defaultModel;
	});

	function matchesModel(m: ModelEntry, q: string): boolean {
		return [m.provider, m.id, m.name, m.alias ?? '', m.api ?? '']
			.join(' ')
			.toLowerCase()
			.includes(q);
	}

	function ctxLabel(window?: number): string {
		if (!window) return '';
		if (window >= 1_000_000) return `${(window / 1_000_000).toFixed(window % 1_000_000 === 0 ? 0 : 1)}M`;
		if (window >= 1_000) return `${(window / 1_000).toFixed(window % 1_000 === 0 ? 0 : 1)}K`;
		return String(window);
	}

	function trimFixed(value: number, digits: number): string {
		return value.toFixed(digits).replace(/\.?0+$/, '');
	}

	function formatPrice(value: number | undefined): string {
		if (value == null) return '—';
		if (value === 0) return '$0';
		return `$${trimFixed(value, value < 1 ? 3 : 2)}`;
	}

	function priceSourceLabel(source?: string): string {
		if (source === 'priceUsdPer1M') return 'override';
		if (source === 'builtin') return 'built-in';
		if (source === 'catalog') return 'catalog';
		return 'unknown';
	}
</script>

<section class="panel" aria-labelledby="models-title">
	<header class="head">
		<h1 id="models-title" class="title">Models</h1>
		<span class="t-mono dim">
			{shownCount}/{totalCount} model{totalCount === 1 ? '' : 's'} · {pricedCount} priced · {reasoningCount} reasoning
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
		<section class="toolbar" aria-label="Model controls">
			<label class="search">
				<span class="t-label">Search</span>
				<input
					class="search-input"
					type="search"
					placeholder="provider, id, alias"
					bind:value={query}
				/>
			</label>
			<div class="current-default">
				<span class="t-label">Default</span>
				<span class="t-mono default-text">{defaultLabel}</span>
			</div>
		</section>

		{#if defaultError}
			<p class="top-error t-mono" role="status">{defaultError}</p>
		{/if}

		{#if shownCount === 0}
			<div class="t-mono dim">No models match "{query}".</div>
		{/if}

		{#each groups as g (g.provider)}
			<section class="block" aria-labelledby="provider-{g.provider}">
				<header class="block-head">
					<h2 id="provider-{g.provider}" class="block-title">
						{g.provider}
						<span class="t-mono provider-count">{g.entries.length}</span>
						<span class="t-label badge {g.authOk ? 'ok' : 'bad'}">
							{g.authOk ? 'auth ok' : 'no auth'}
						</span>
					</h2>
				</header>
				<table class="models-table">
					<thead>
						<tr>
							<th scope="col">id</th>
							<th scope="col">name</th>
							<th scope="col" class="num">ctx</th>
							<th scope="col">price / 1M</th>
							<th scope="col">reasoning</th>
							<th scope="col">default</th>
							<th scope="col">alias</th>
						</tr>
					</thead>
					<tbody>
						{#each g.entries as m (m.id)}
							{@const key = modelKey(m)}
							<tr class={!m.authOk ? 'unauthed' : ''}>
								<td class="t-mono id" data-label="id">{m.provider}/{m.id}</td>
								<td data-label="name">{m.name}</td>
								<td class="t-num num" data-label="ctx">{ctxLabel(m.contextWindow)}</td>
								<td class="price-cell" data-label="price / 1M">
									<div class="price-line">
										<span class="dim">in</span>
										<span class="t-num">{formatPrice(m.cost?.input)}</span>
									</div>
									<div class="price-line">
										<span class="dim">out</span>
										<span class="t-num">{formatPrice(m.cost?.output)}</span>
									</div>
									<div class="price-source t-mono">{priceSourceLabel(m.cost?.source)}</div>
								</td>
								<td data-label="reasoning">{m.reasoning ? 'yes' : ''}</td>
								<td class="default-cell" data-label="default">
									{#if key === defaultModel}
										<span class="t-label default-badge">default</span>
									{:else}
										<button
											type="button"
											class="default-btn"
											onclick={() => makeDefault(m)}
											disabled={defaultSaving[key]}
										>
											{defaultSaving[key] ? 'Saving…' : 'Set'}
										</button>
									{/if}
								</td>
								<td class="alias-cell" data-label="alias">
									<div class="alias-row">
										<input
											class="alias-input t-mono"
											type="text"
											placeholder="(no alias)"
											aria-label="Alias for {m.provider}/{m.id}"
											bind:value={aliasDrafts[key]}
											oninput={() => scheduleSave(m)}
											onblur={() => flushSave(m)}
										/>
										{#if aliasSaving[key]}
											<span class="alias-status t-mono" aria-live="polite">saving…</span>
										{/if}
									</div>
									{#if aliasError[key]}
										<div class="alias-err t-mono" role="status">{aliasError[key]}</div>
									{/if}
								</td>
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
	.top-error {
		margin: 0;
		color: var(--accent-strong, var(--ink));
		font-size: var(--fs-xs);
	}

	.toolbar {
		display: flex;
		align-items: end;
		gap: var(--s-4);
		flex-wrap: wrap;
		border-bottom: 1px solid var(--border);
		padding-bottom: var(--s-4);
	}
	.search {
		display: flex;
		flex-direction: column;
		gap: 4px;
		min-width: min(260px, 100%);
	}
	.search-input {
		background: var(--canvas);
		border: 1px solid var(--border);
		border-radius: var(--radius);
		color: var(--ink);
		font-size: var(--fs-sm);
		min-height: 32px;
		padding: 4px var(--s-2);
	}
	.search-input:focus-visible {
		outline: 2px solid var(--accent);
		outline-offset: 1px;
	}
	.current-default {
		display: grid;
		gap: 4px;
		min-width: min(360px, 100%);
	}
	.default-text {
		color: var(--ink);
		word-break: break-all;
	}

	.block {
		display: flex;
		flex-direction: column;
		gap: var(--s-3);
		overflow-x: auto;
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
	.provider-count {
		color: var(--ink-3);
		font-size: var(--fs-xs);
		font-weight: 400;
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
		min-width: 920px;
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
	.price-cell {
		min-width: 116px;
	}
	.price-line {
		display: flex;
		justify-content: space-between;
		gap: var(--s-2);
		max-width: 104px;
	}
	.price-source {
		color: var(--ink-3);
		font-size: var(--fs-xs);
		margin-top: 2px;
	}
	.default-cell {
		min-width: 84px;
	}
	.default-badge {
		color: var(--accent);
		border: 1px solid var(--accent);
		border-radius: var(--radius);
		padding: 2px 6px;
		font-size: var(--fs-xs);
	}
	.default-btn {
		background: var(--canvas);
		border: 1px solid var(--border);
		border-radius: var(--radius);
		color: var(--ink);
		cursor: pointer;
		font-size: var(--fs-xs);
		min-height: 28px;
		padding: 3px var(--s-2);
	}
	.default-btn:disabled {
		opacity: 0.6;
		cursor: not-allowed;
	}
	.default-btn:focus-visible {
		outline: 2px solid var(--accent);
		outline-offset: 1px;
	}
	.models-table tr.unauthed td {
		color: var(--ink-3);
	}
	.alias-cell {
		min-width: 160px;
	}
	.alias-row {
		display: inline-flex;
		gap: var(--s-2);
		align-items: center;
		width: 100%;
	}
	.alias-input {
		background: var(--canvas);
		border: 1px solid var(--border);
		border-radius: var(--radius);
		padding: 3px var(--s-2);
		font-size: var(--fs-xs);
		color: var(--ink);
		min-height: 28px;
		flex: 1;
		min-width: 0;
	}
	.alias-input:focus-visible {
		outline: 2px solid var(--accent);
		outline-offset: 1px;
	}
	.alias-status {
		font-size: var(--fs-xs);
		color: var(--ink-3);
		white-space: nowrap;
	}
	.alias-err {
		font-size: var(--fs-xs);
		color: var(--accent-strong, var(--ink));
		margin-top: 2px;
	}

	@media (max-width: 720px) {
		/* Mobile: stack table rows as cards; the multi-column
		   layout is unusable below ~600px wide. Sticky header gone. */
		.models-table thead {
			display: none;
		}
		.models-table {
			min-width: 0;
		}
		.models-table tbody,
		.models-table tr,
		.models-table td {
			display: block;
			width: 100%;
		}
		.models-table tr {
			border: 1px solid var(--border);
			border-radius: var(--radius);
			padding: var(--s-2);
			margin-bottom: var(--s-2);
		}
		.models-table td {
			border: 0;
			display: grid;
			gap: var(--s-2);
			grid-template-columns: 92px minmax(0, 1fr);
			padding: 3px 0;
		}
		.models-table td::before {
			content: attr(data-label);
			color: var(--ink-3);
			font-size: var(--fs-xs);
			font-weight: 700;
			text-transform: lowercase;
		}
		.models-table .num {
			text-align: left;
		}
		.price-line {
			max-width: 120px;
		}
		.alias-cell {
			margin-top: var(--s-2);
		}
		.alias-row {
			align-items: stretch;
			flex-direction: column;
		}
	}

	@media (max-width: 720px) {
		.panel {
			padding: var(--s-4) var(--s-3);
		}
	}
</style>
