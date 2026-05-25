<script lang="ts">
	// Recursive JSON tree. Renders objects + arrays with click-to-
	// expand sections; scalars render inline. Top-level value is the
	// only one expanded by default so the user sees the structure
	// without being buried in detail.
	//
	// Self-import (per Svelte 5 deprecation guidance) keeps the
	// recursion explicit. Max depth in practice for talon config is
	// ~6 (gateway.auth.password.deriveKey style); if a future
	// config explodes deeper, switch to an iterative flatten.

	import Self from './JSONTree.svelte';

	let {
		value,
		name = '',
		path = '',
		depth = 0,
	}: {
		value: unknown;
		name?: string;
		path?: string;
		depth?: number;
	} = $props();

	const kind = $derived(detectKind(value));
	const childPath = $derived(name ? (path ? `${path}.${name}` : name) : path);
	// Top of the tree (depth 0) defaults open so the user sees the
	// shape; deeper sections collapse so the page doesn't drown.
	// Reading $props().depth here would warn about local capture;
	// `depth` is set once per node so the initial snapshot is what
	// we want.
	let open = $state(false);
	$effect(() => {
		// Initialize once from the prop. Doesn't re-fire on user
		// toggles because effects only re-run on dep changes, and
		// `depth` is stable per-node.
		open = depth === 0;
	});

	function detectKind(v: unknown): 'null' | 'bool' | 'number' | 'string' | 'array' | 'object' {
		if (v === null) return 'null';
		if (Array.isArray(v)) return 'array';
		const t = typeof v;
		if (t === 'boolean') return 'bool';
		if (t === 'number') return 'number';
		if (t === 'string') return 'string';
		if (t === 'object') return 'object';
		return 'string';
	}

	function formatScalar(v: unknown): string {
		if (v === null) return 'null';
		if (typeof v === 'string') return JSON.stringify(v);
		return String(v);
	}

	const entries = $derived.by(() => {
		if (kind === 'array') return (value as unknown[]).map((v, i) => [String(i), v] as const);
		if (kind === 'object') return Object.entries(value as Record<string, unknown>);
		return [] as Array<readonly [string, unknown]>;
	});

	const summary = $derived.by(() => {
		if (kind === 'array') return `[${(value as unknown[]).length}]`;
		if (kind === 'object') {
			const keys = Object.keys(value as object);
			return `{${keys.length}}`;
		}
		return '';
	});
</script>

{#if kind === 'object' || kind === 'array'}
	<div class="branch" style:--depth={depth}>
		<button
			type="button"
			class="row toggle"
			aria-expanded={open}
			onclick={() => (open = !open)}
		>
			<span class="caret" aria-hidden="true">{open ? '▾' : '▸'}</span>
			{#if name}<span class="key t-mono">{name}</span>{/if}
			<span class="kind t-num dim">{summary}</span>
		</button>
		{#if open}
			<div class="children" role="group" aria-label={childPath || 'root'}>
				{#each entries as [childName, childValue] (childName)}
					<Self
						value={childValue}
						name={childName}
						path={childPath}
						depth={depth + 1}
					/>
				{/each}
			</div>
		{/if}
	</div>
{:else}
	<div class="branch leaf" style:--depth={depth}>
		<div class="row">
			{#if name}<span class="key t-mono">{name}</span>{/if}
			<span class="value t-mono kind-{kind}">{formatScalar(value)}</span>
		</div>
	</div>
{/if}

<style>
	.branch {
		padding-left: calc(var(--depth, 0) * var(--s-4));
	}
	.row {
		display: flex;
		align-items: baseline;
		gap: var(--s-2);
		min-height: 24px;
		padding: 2px 0;
		font-family: var(--ff-mono);
		font-size: var(--fs-sm);
		line-height: var(--lh-snug);
		color: var(--ink-2);
	}
	.row.toggle {
		background: transparent;
		border: 0;
		width: 100%;
		text-align: left;
		font: inherit;
		color: inherit;
		cursor: pointer;
		padding-left: 0;
	}
	.row.toggle:hover {
		color: var(--ink);
	}
	.row.toggle:focus-visible {
		outline: 2px solid var(--accent);
		outline-offset: 2px;
		border-radius: var(--radius);
	}
	.caret {
		width: 12px;
		color: var(--ink-3);
		flex-shrink: 0;
	}
	.key {
		color: var(--ink);
		font-weight: 700;
	}
	.kind {
		color: var(--ink-3);
	}
	.dim {
		color: var(--ink-3);
	}
	.value {
		color: var(--ink);
	}
	.kind-null {
		color: var(--ink-3);
		font-style: italic;
	}
	.kind-string {
		color: var(--accent-strong, var(--accent));
		word-break: break-all;
		min-width: 0;
	}
	.kind-number,
	.kind-bool {
		color: var(--ink);
	}
	.children {
		border-left: 1px solid var(--border);
		margin-left: 5px;
		padding-left: 0;
	}
</style>
