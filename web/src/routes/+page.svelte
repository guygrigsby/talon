<script lang="ts">
	import ChannelRail from '$lib/components/ChannelRail.svelte';
	import Transcript from '$lib/components/Transcript.svelte';
	import Inspector from '$lib/components/Inspector.svelte';
	import { channels, messages } from '$lib/data/channels';
	import { chrome } from '$lib/state/chrome.svelte';

	let activeId = $state('web-here');

	const channel = $derived(channels.find((c) => c.id === activeId) ?? channels[0]);
	const stream = $derived(messages[channel.id] ?? []);

	function selectChannel(id: string) {
		activeId = id;
		if (chrome.isNarrow) chrome.railOpen = false;
	}
</script>

<ChannelRail
	{activeId}
	open={chrome.railOpen}
	onSelect={selectChannel}
	onClose={() => (chrome.railOpen = false)}
/>
<Transcript
	{channel}
	messages={stream}
	inspectorOpen={chrome.inspectorOpen}
	onToggleInspector={() => chrome.toggleInspector()}
/>
<Inspector
	{channel}
	messages={stream}
	open={chrome.inspectorOpen}
	onClose={() => (chrome.inspectorOpen = false)}
/>

{#if chrome.isNarrow && (chrome.railOpen || chrome.inspectorOpen)}
	<button
		type="button"
		class="backdrop"
		aria-label="Close panel"
		onclick={() => chrome.closeAllOnNarrow()}
	></button>
{/if}

<style>
	.backdrop {
		position: fixed;
		inset: var(--topbar-h) 0 var(--statusbar-h) 0;
		background: rgba(24, 24, 26, 0.28);
		backdrop-filter: blur(2px);
		z-index: 40;
		border: 0;
		padding: 0;
		cursor: pointer;
	}
</style>
