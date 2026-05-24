<script lang="ts">
	import Wordmark from './Wordmark.svelte';
	import { navTabs } from '$lib/data/sections';

	let {
		current = 'chat',
		onToggleRail,
		onOpenPalette,
		railOpen = false,
	}: {
		current?: string;
		onToggleRail?: () => void;
		onOpenPalette?: () => void;
		railOpen?: boolean;
	} = $props();
</script>

<header class="top">
	<button
		type="button"
		class="ham"
		aria-label={railOpen ? 'Close channels' : 'Open channels'}
		aria-expanded={railOpen}
		aria-controls="channel-rail"
		onclick={() => onToggleRail?.()}
	>
		<svg viewBox="0 0 24 24" width="20" height="20" aria-hidden="true" focusable="false">
			{#if railOpen}
				<path d="M5 5 L 19 19 M 19 5 L 5 19" stroke="currentColor" stroke-width="2" fill="none" />
			{:else}
				<path d="M3 7 H 21 M 3 12 H 21 M 3 17 H 21" stroke="currentColor" stroke-width="2" fill="none" />
			{/if}
		</svg>
	</button>

	<div class="left">
		<Wordmark />
		<span class="tag hide-sm" aria-hidden="true">personal gateway</span>
	</div>

	<nav class="nav" aria-label="Primary">
		{#each navTabs as tab (tab.key)}
			<a
				href={tab.href}
				class="tab"
				aria-current={current === tab.key ? 'page' : undefined}
			>
				{tab.label}
			</a>
		{/each}
	</nav>

	<div class="right">
		<button type="button" class="cmd" onclick={() => onOpenPalette?.()} aria-haspopup="dialog">
			<span class="cmd-label hide-sm">Search</span>
			<kbd class="key t-mono">⌘K</kbd>
		</button>
	</div>
</header>

<style>
	.top {
		display: flex;
		align-items: center;
		gap: var(--s-4);
		height: var(--topbar-h);
		padding: 0 var(--s-4) 0 var(--s-3);
		background: var(--canvas);
		color: var(--ink);
		border-bottom: 1px solid var(--border);
	}

	.ham {
		display: none;
		align-items: center;
		justify-content: center;
		width: var(--tap);
		height: var(--tap);
		border-radius: var(--radius-sm);
		color: var(--ink-2);
	}
	.ham:hover {
		color: var(--ink);
		background: var(--surface-2);
	}

	.left {
		display: inline-flex;
		align-items: center;
		gap: var(--s-3);
		flex-shrink: 0;
	}
	.tag {
		font-size: var(--fs-xs);
		color: var(--ink-3);
		padding-left: var(--s-3);
		border-left: 1px solid var(--border);
		letter-spacing: var(--tracking-loose);
	}

	.nav {
		display: flex;
		gap: var(--s-1);
		margin-left: var(--s-2);
		height: 100%;
		flex: 1 1 auto;
		min-width: 0;
		overflow-x: auto;
		scrollbar-width: none;
	}
	.nav::-webkit-scrollbar {
		display: none;
	}
	.tab {
		display: inline-flex;
		align-items: center;
		padding: 0 var(--s-3);
		font-size: var(--fs-sm);
		font-weight: 700;
		color: var(--ink-3);
		border-bottom: 2px solid transparent;
		min-height: var(--tap);
		flex-shrink: 0;
		white-space: nowrap;
	}
	.tab:hover {
		color: var(--ink);
	}
	.tab[aria-current='page'] {
		color: var(--accent);
		border-bottom-color: var(--accent);
	}

	.right {
		margin-left: auto;
		display: inline-flex;
		align-items: center;
		flex-shrink: 0;
	}
	.cmd {
		display: inline-flex;
		align-items: center;
		gap: var(--s-2);
		height: 36px;
		padding: 0 var(--s-2) 0 var(--s-3);
		border: 1px solid var(--border-strong);
		border-radius: var(--radius);
		background: var(--surface);
		color: var(--ink-2);
	}
	.cmd:hover {
		border-color: var(--accent-edge);
		color: var(--ink);
	}
	.cmd-label {
		font-size: var(--fs-sm);
	}
	.key {
		display: inline-flex;
		align-items: center;
		font-size: var(--fs-xs);
		font-weight: 700;
		color: var(--ink-3);
		background: var(--surface);
		border: 1px solid var(--border);
		border-radius: var(--radius-sm);
		padding: 2px 5px;
	}

	@media (max-width: 720px) {
		.ham {
			display: inline-flex;
		}
		.hide-sm {
			display: none !important;
		}
		.cmd {
			height: var(--tap);
			width: var(--tap);
			justify-content: center;
			padding: 0;
		}
	}
</style>
