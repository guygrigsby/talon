<script lang="ts">
	// Config view: read the merged talon config via
	// ConfigService.Get("") and render as a clickable tree. Writes
	// land in a follow-up; this read-only pass closes the gap where
	// the user had to inspect config files by hand to see what was loaded.

	import { create } from '@bufbuild/protobuf';
	import { ConfigGetRequestSchema } from '$lib/gateway/gen/talon/v1/config_pb.js';
	import { getConfigClient } from '$lib/gateway/connect';
	import JSONTree from '$lib/components/JSONTree.svelte';

	let raw = $state<unknown | null>(null);
	let loadError = $state<string | null>(null);
	let loading = $state(true);
	let filter = $state('');

	// ConfigService.Get("") returns a status envelope:
	//   {config:{...}, parsed, raw, path, hash, exists, valid, issues}
	// The user wants to inspect the merged config, so unwrap to the
	// `config` field. The other envelope fields show as a small
	// status row above the tree.
	type ConfigEnvelope = {
		config?: unknown;
		path?: string;
		hash?: string;
		exists?: boolean;
		valid?: boolean;
		issues?: unknown[];
	};

	let envelope = $state<ConfigEnvelope | null>(null);

	async function load() {
		loading = true;
		loadError = null;
		try {
			const res = await getConfigClient().get(create(ConfigGetRequestSchema, { path: '' }));
			const parsed = res.json ? (JSON.parse(res.json) as ConfigEnvelope) : null;
			envelope = parsed;
			raw = parsed?.config ?? null;
		} catch (err) {
			loadError = err instanceof Error ? err.message : String(err);
		} finally {
			loading = false;
		}
	}

	$effect(() => {
		load();
	});

	// Filter operates on the dotted-key namespace. When non-empty,
	// derive a subtree that matches: keep keys whose dotted path
	// contains the filter substring (case-insensitive). Implemented
	// as a flat key→value map so the tree shows just the matched
	// branches without rebuilding the whole nested shape.
	const filtered = $derived.by(() => {
		const root = raw;
		if (!filter || root == null || typeof root !== 'object') return root;
		const needle = filter.toLowerCase();
		const out: Record<string, unknown> = {};
		walk(root as Record<string, unknown>, '', (path, value) => {
			if (path.toLowerCase().includes(needle)) {
				out[path] = value;
			}
		});
		return out;
	});

	function walk(
		node: Record<string, unknown>,
		prefix: string,
		visit: (path: string, value: unknown) => void
	) {
		for (const [k, v] of Object.entries(node)) {
			const p = prefix ? `${prefix}.${k}` : k;
			visit(p, v);
			if (v && typeof v === 'object' && !Array.isArray(v)) {
				walk(v as Record<string, unknown>, p, visit);
			}
		}
	}
</script>

<section class="panel" aria-labelledby="config-title">
	<header class="head">
		<h1 id="config-title" class="title">Config</h1>
		<span class="t-mono dim">merged talon overlay</span>
		<button type="button" class="op" onclick={load} disabled={loading}>
			{loading ? 'Loading…' : 'Refresh'}
		</button>
	</header>
	{#if envelope}
		<div class="status t-mono">
			<span class="dim">path</span>
			<span>{envelope.path ?? '(none)'}</span>
			<span class="dim sep">·</span>
			<span class="dim">hash</span>
			<span title={envelope.hash}>{envelope.hash?.slice(0, 12) ?? '?'}</span>
			<span class="dim sep">·</span>
			<span class={envelope.valid ? 'ok' : 'bad'}>
				{envelope.valid ? 'valid' : 'invalid'}
			</span>
			{#if envelope.issues && envelope.issues.length > 0}
				<span class="dim sep">·</span>
				<span class="bad">{envelope.issues.length} issue{envelope.issues.length === 1 ? '' : 's'}</span>
			{/if}
		</div>
	{/if}

	<div class="bar">
		<label class="filter">
			<span class="t-label sr-only">Filter</span>
			<input
				type="search"
				class="t-mono"
				placeholder="filter by dotted path (e.g. gateway.auth, agents.list)"
				bind:value={filter}
				aria-label="Filter config by path"
			/>
		</label>
	</div>

	{#if loadError}
		<div class="err t-mono" role="status">{loadError}</div>
	{:else if loading && raw == null}
		<div class="t-mono dim">Loading config…</div>
	{:else if raw == null}
		<div class="t-mono dim">No config returned.</div>
	{:else}
		<div class="tree">
			<JSONTree value={filtered} />
		</div>
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
		gap: var(--s-4);
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
	.bar {
		display: flex;
		gap: var(--s-3);
	}
	.filter {
		flex: 1;
		display: block;
	}
	.filter input {
		width: 100%;
		background: var(--canvas);
		border: 1px solid var(--border);
		border-radius: var(--radius);
		padding: 8px var(--s-3);
		font-size: var(--fs-sm);
		color: var(--ink);
		min-height: var(--tap, 32px);
	}
	.filter input:focus-visible {
		outline: 2px solid var(--accent);
		outline-offset: 2px;
	}
	.sr-only {
		position: absolute;
		width: 1px;
		height: 1px;
		padding: 0;
		margin: -1px;
		overflow: hidden;
		clip: rect(0, 0, 0, 0);
		white-space: nowrap;
		border: 0;
	}
	.tree {
		border: 1px solid var(--border);
		border-radius: var(--radius);
		padding: var(--s-3);
		background: var(--canvas);
		flex: 1;
		min-height: 0;
		overflow: auto;
	}
	.err {
		color: var(--accent-strong, var(--ink));
		font-size: var(--fs-sm);
	}
	.status {
		display: flex;
		gap: var(--s-2);
		align-items: baseline;
		font-size: var(--fs-xs);
		color: var(--ink-2);
	}
	.status .sep {
		color: var(--ink-3);
	}
	.status .ok {
		color: var(--good, var(--accent));
	}
	.status .bad {
		color: var(--accent-strong, var(--ink));
	}

	@media (max-width: 720px) {
		.panel {
			padding: var(--s-4) var(--s-3);
		}
	}
</style>
