<script lang="ts">
	import '$lib/design/base.css';
	import { page } from '$app/state';
	import TopBar from '$lib/components/TopBar.svelte';
	import StatusBar from '$lib/components/StatusBar.svelte';
	import CommandPalette from '$lib/components/CommandPalette.svelte';
	import { chrome } from '$lib/state/chrome.svelte';

	let { children } = $props();

	const current = $derived(page.url.pathname.split('/')[1] || 'chat');

	$effect(() => {
		chrome.startWatching();
		return () => chrome.stopWatching();
	});

	function onWindowKeydown(e: KeyboardEvent) {
		// ⌘K / Ctrl+K toggles the palette from anywhere
		if ((e.metaKey || e.ctrlKey) && (e.key === 'k' || e.key === 'K')) {
			e.preventDefault();
			chrome.togglePalette();
			return;
		}
		if (e.key === 'Escape') {
			if (chrome.paletteOpen) chrome.closePalette();
			else chrome.closePanelsOnNarrow();
		}
	}
</script>

<svelte:head>
	<title>talon</title>
	<meta name="theme-color" content="#ffffff" />
</svelte:head>

<svelte:window onkeydown={onWindowKeydown} />

<a class="skip-link" href="#main">Skip to content</a>

<div class="shell">
	<TopBar
		{current}
		onOpenPalette={() => chrome.openPalette()}
	/>
	<main id="main" class="body" tabindex="-1">
		{@render children()}
	</main>
	<StatusBar />
</div>

<CommandPalette open={chrome.paletteOpen} onClose={() => chrome.closePalette()} />

<style>
	.shell {
		display: grid;
		grid-template-rows: var(--topbar-h) 1fr var(--statusbar-h);
		height: 100dvh;
		width: 100vw;
		background: var(--canvas);
		overflow: hidden;
	}
	.body {
		display: flex;
		min-height: 0;
		overflow: hidden;
		position: relative;
	}
	.body:focus-visible {
		outline: none;
		box-shadow: none;
	}
</style>
