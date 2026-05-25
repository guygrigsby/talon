<script lang="ts">
	import ChannelRail from '$lib/components/ChannelRail.svelte';
	import Transcript from '$lib/components/Transcript.svelte';
	import Inspector from '$lib/components/Inspector.svelte';
	import { channels, messages } from '$lib/data/channels';
	import { chrome } from '$lib/state/chrome.svelte';
	import { makeChatStore } from '$lib/gateway/chatStore.svelte';

	let activeId = $state('web-here');

	const channel = $derived(channels.find((c) => c.id === activeId) ?? channels[0]);

	// First live wire: the 'web-here' channel maps to a fixed
	// gateway session-key. Other channels stay on mock data until
	// their respective bridges (telegram, signal, ...) get plumbed
	// through the gateway too. The agent ID after the colon is
	// what the gateway resolves to a model+workspace; if it isn't
	// configured locally History returns empty and Send returns a
	// typed error which the composer surfaces inline.
	const LIVE_CHANNEL_ID = 'web-here';
	const LIVE_SESSION_KEY = 'agent:talon:web';

	const liveStore = makeChatStore(LIVE_SESSION_KEY);

	$effect(() => {
		// Mount the live history + subscribe loop with the page.
		// loadHistory backfills once; startSubscribe stays open
		// until the returned dispose() runs on unmount.
		liveStore.loadHistory();
		liveStore.startSubscribe();
		return () => liveStore.dispose();
	});

	const isLive = $derived(channel.id === LIVE_CHANNEL_ID);
	const stream = $derived(isLive ? liveStore.messages : (messages[channel.id] ?? []));
	const composerStatus = $derived(isLive ? liveStore.status : 'idle');
	const composerError = $derived(isLive ? liveStore.errorMessage : null);
	const onSend = $derived(isLive ? (text: string) => liveStore.send(text) : undefined);

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
	{onSend}
	status={composerStatus}
	errorMessage={composerError}
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
