<script lang="ts">
	import ChannelRail from '$lib/components/ChannelRail.svelte';
	import Transcript from '$lib/components/Transcript.svelte';
	import Inspector from '$lib/components/Inspector.svelte';
	import { channels, messages } from '$lib/data/channels';
	import { chrome } from '$lib/state/chrome.svelte';
	import { makeChatStore } from '$lib/gateway/chatStore.svelte';
	import { getAgentsClient } from '$lib/gateway/connect';

	let activeId = $state('web-here');

	const channel = $derived(channels.find((c) => c.id === activeId) ?? channels[0]);

	// 'web-here' is the channel wired to the live gateway. Session-key
	// shape is `agent:<id>:<conv>`; the agentId is resolved from
	// agents.list at mount so we don't hardcode an agent name that
	// might not exist on this gateway. <conv> stays "web" so reloads
	// land back in the same conversation.
	const LIVE_CHANNEL_ID = 'web-here';
	const LIVE_CONV = 'web';

	let agentId = $state<string | null>(null);
	let agentsLoadError = $state<string | null>(null);
	let liveStore: ReturnType<typeof makeChatStore> | null = $state(null);

	async function loadAgents() {
		try {
			const client = getAgentsClient();
			const res = await client.list({});
			const parsed = JSON.parse(res.json);
			const def = typeof parsed.defaultId === 'string' ? parsed.defaultId : '';
			const first = Array.isArray(parsed.agents) && parsed.agents[0]?.id;
			agentId = def || first || null;
			if (!agentId) {
				agentsLoadError = 'No agents configured on this gateway.';
			}
		} catch (err) {
			agentsLoadError = err instanceof Error ? err.message : String(err);
		}
	}

	$effect(() => {
		loadAgents();
	});

	// (Re)create the chat store any time the agentId resolves to a
	// new value. Mount the history + subscribe loop on the new
	// store, dispose the old one in the cleanup so the prior stream
	// doesn't leak across agent changes.
	$effect(() => {
		if (!agentId) return;
		const store = makeChatStore(`agent:${agentId}:${LIVE_CONV}`);
		liveStore = store;
		store.loadHistory();
		store.startSubscribe();
		return () => {
			store.dispose();
			if (liveStore === store) liveStore = null;
		};
	});

	const isLive = $derived(channel.id === LIVE_CHANNEL_ID);
	const stream = $derived.by(() => {
		const s = liveStore;
		if (isLive && s) return s.messages;
		return messages[channel.id] ?? [];
	});
	const composerStatus = $derived.by(() => {
		if (!isLive) return 'idle' as const;
		const s = liveStore;
		if (s) return s.status;
		return agentsLoadError ? ('error' as const) : ('loading' as const);
	});
	const composerError = $derived.by(() => {
		if (!isLive) return null;
		const s = liveStore;
		return s?.errorMessage ?? agentsLoadError;
	});
	const onSend = $derived.by(() => {
		const s = liveStore;
		if (!isLive || !s) return undefined;
		return (text: string) => s.send(text);
	});

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
