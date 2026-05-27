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
	let menuOpen = $state(false);

	function openSearch() {
		menuOpen = false;
		onOpenPalette?.();
	}

	function onWindowKeydown(e: KeyboardEvent) {
		if (menuOpen && e.key === 'Escape') menuOpen = false;
	}
</script>

<svelte:window onkeydown={onWindowKeydown} />

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
			class="cmd desktop-search"
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
		<button
			type="button"
			class="menu-toggle mobile-only"
			onclick={() => (menuOpen = !menuOpen)}
			aria-expanded={menuOpen}
			aria-haspopup="menu"
			aria-label={menuOpen ? 'Close menu' : 'Open menu'}
		>
			<svg viewBox="0 0 24 24" width="20" height="20" aria-hidden="true">
				{#if menuOpen}
					<path d="M6 6 L 18 18 M 18 6 L 6 18" stroke="currentColor" stroke-width="2" stroke-linecap="round" fill="none" />
				{:else}
					<path d="M4 7 H 20 M 4 12 H 20 M 4 17 H 20" stroke="currentColor" stroke-width="2" stroke-linecap="round" fill="none" />
				{/if}
			</svg>
		</button>
	</div>
</header>

{#if menuOpen}
	<button
		type="button"
		class="menu-scrim mobile-only"
		aria-label="Close menu"
		onclick={() => (menuOpen = false)}
	></button>
	<div class="mobile-menu mobile-only" role="dialog" aria-modal="true" aria-label="Navigation menu">
		<button type="button" class="menu-search" onclick={openSearch}>
			<svg viewBox="0 0 24 24" width="18" height="18" aria-hidden="true">
				<circle cx="11" cy="11" r="6.5" stroke="currentColor" stroke-width="2" fill="none" />
				<path d="M16 16 L 21 21" stroke="currentColor" stroke-width="2" fill="none" />
			</svg>
			<span>Search</span>
			<kbd class="t-mono">⌘K</kbd>
		</button>

		<nav class="menu-nav" aria-label="Primary">
			{#each navTabs as tab (tab.key)}
				<a
					href={tab.href}
					class="menu-link"
					class:active={current === tab.key}
					aria-current={current === tab.key ? 'page' : undefined}
					onclick={() => (menuOpen = false)}
				>
					<span>{tab.label}</span>
					{#if current === tab.key}
						<span class="menu-current t-mono">current</span>
					{/if}
				</a>
			{/each}
		</nav>
	</div>
{/if}

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
		position: relative;
		z-index: 90;
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
	.mobile-only {
		display: none;
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
		.desktop-search {
			display: none;
		}
		.mobile-only {
			display: flex;
		}
		.hide-sm {
			display: none !important;
		}
		.menu-toggle {
			align-items: center;
			justify-content: center;
			height: var(--tap);
			width: var(--tap);
			padding: 0;
			border: 0;
			background: transparent;
			color: var(--ink-2);
			border-radius: var(--radius-sm);
		}
		.menu-toggle:hover {
			color: var(--ink);
			background: var(--surface-2);
		}
		.key {
			display: none;
		}
		.menu-scrim {
			position: fixed;
			inset: var(--topbar-h) 0 0;
			z-index: 70;
			background: rgba(24, 24, 26, 0.22);
			backdrop-filter: blur(2px);
			border: 0;
			padding: 0;
		}
		.mobile-menu {
			position: fixed;
			top: var(--topbar-h);
			left: var(--s-2);
			right: var(--s-2);
			z-index: 80;
			display: flex;
			flex-direction: column;
			background: var(--surface);
			border: 1px solid var(--border-strong);
			border-radius: 8px;
			box-shadow: var(--shadow-pop);
			overflow: hidden;
		}
		.menu-search {
			display: flex;
			align-items: center;
			gap: var(--s-3);
			min-height: var(--tap);
			padding: 0 var(--s-4);
			color: var(--ink);
			font-weight: 700;
			border-bottom: 1px solid var(--border);
			text-align: left;
		}
		.menu-search svg {
			color: var(--ink-3);
			flex-shrink: 0;
		}
		.menu-search kbd {
			margin-left: auto;
			font-size: var(--fs-xs);
			font-weight: 700;
			color: var(--ink-3);
			border: 1px solid var(--border);
			border-radius: var(--radius-sm);
			padding: 2px 6px;
		}
		.menu-nav {
			display: flex;
			flex-direction: column;
			padding: var(--s-2);
		}
		.menu-link {
			display: flex;
			align-items: center;
			justify-content: space-between;
			gap: var(--s-3);
			min-height: var(--tap);
			padding: 0 var(--s-3);
			border-radius: var(--radius-sm);
			color: var(--ink-2);
			font-size: var(--fs-md);
			font-weight: 700;
			text-transform: lowercase;
		}
		.menu-link.active {
			color: var(--accent);
			background: var(--accent-tint);
		}
		.menu-current {
			color: var(--accent-strong);
			font-size: var(--fs-xs);
		}
	}
</style>
