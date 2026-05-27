<script lang="ts">
	import Wordmark from './Wordmark.svelte';
	import { navTabs } from '$lib/data/sections';

	let {
		current = 'chat',
		onOpenPalette,
	}: {
		current?: string;
		onOpenPalette?: () => void;
	} = $props();

	const currentLabel = $derived(navTabs.find((tab) => tab.key === current)?.label ?? current);
</script>

<header class="top">
	<div class="left">
		<Wordmark />
		<span class="tag hide-sm" aria-hidden="true">personal gateway</span>
	</div>

	<div class="mobile-context" aria-hidden="true">{currentLabel}</div>

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
		<button
			type="button"
			class="cmd"
			onclick={() => onOpenPalette?.()}
			aria-haspopup="dialog"
			aria-label="Open command palette"
			title="Open command palette"
		>
			<svg class="cmd-icon" viewBox="0 0 24 24" width="18" height="18" aria-hidden="true">
				<circle cx="11" cy="11" r="6.5" stroke="currentColor" stroke-width="2" fill="none" />
				<path d="M16 16 L 21 21" stroke="currentColor" stroke-width="2" fill="none" />
			</svg>
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

	.left {
		display: inline-flex;
		align-items: center;
		gap: var(--s-3);
		flex-shrink: 0;
	}
	.mobile-context {
		display: none;
		font-size: var(--fs-sm);
		font-weight: 700;
		color: var(--ink);
		text-transform: lowercase;
		min-width: 0;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
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
	.cmd-icon {
		display: none;
		flex-shrink: 0;
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
		.top {
			display: grid;
			grid-template-columns: var(--tap) 1fr var(--tap);
			gap: 0;
			padding: 0 var(--s-2);
		}
		.left {
			justify-content: center;
			width: var(--tap);
		}
		.left :global(.word) {
			display: none;
		}
		.mobile-context {
			display: block;
			justify-self: center;
			max-width: 100%;
		}
		.nav {
			display: none;
		}
		.right {
			margin-left: 0;
			justify-content: flex-end;
		}
		.hide-sm {
			display: none !important;
		}
		.cmd {
			height: var(--tap);
			width: var(--tap);
			justify-content: center;
			padding: 0;
			border: 0;
			background: transparent;
		}
		.cmd-icon {
			display: block;
		}
		.key {
			display: none;
		}
	}
</style>
