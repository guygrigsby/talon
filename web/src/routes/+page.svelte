<script lang="ts">
	import ChannelRail from '$lib/components/ChannelRail.svelte';
	import Transcript from '$lib/components/Transcript.svelte';
	import Inspector from '$lib/components/Inspector.svelte';
	import { channels as staticChannels, messages, type Channel } from '$lib/data/channels';
	import { chrome } from '$lib/state/chrome.svelte';
	import { makeChatStore } from '$lib/gateway/chatStore.svelte';
	import { loadAgents, type AgentEntry } from '$lib/gateway/agents';
	import { loadConfiguredChannels } from '$lib/gateway/channels';

	let activeId = $state('web-here');

	// Rail entries: the always-present 'web-here' web session plus
	// any gateway-configured channels (telegram, bluebubbles, ...).
	// Re-derived whenever the live channels fetch resolves.
	let liveChannels = $state<Channel[]>([]);
	const channels = $derived<Channel[]>([...staticChannels, ...liveChannels]);
	const channel = $derived(channels.find((c) => c.id === activeId) ?? channels[0]);

	async function refreshChannels() {
		try {
			liveChannels = await loadConfiguredChannels();
		} catch (err) {
			// Config read failures are non-fatal — the static web-here
			// entry keeps the rail usable even if the gateway briefly
			// can't enumerate channels. Surface to console so silent
			// breaks are debuggable without a server-side log.
			console.warn('loadConfiguredChannels failed:', err);
			liveChannels = [];
		}
	}

	$effect(() => {
		refreshChannels();
	});

	// 'web-here' is the channel wired to the live gateway. Talon
	// runs ONE primary agent the user talks to; the rest are
	// subagents the primary can delegate to via the subagent tool.
	// Session-key shape stays `agent:<id>:<conv>` for compatibility
	// with the chat-history keying; <conv> stays "web" so reloads
	// land back in the same conversation.
	const LIVE_CHANNEL_ID = 'web-here';
	const LIVE_CONV = 'web';

	let primary = $state<AgentEntry | null>(null);
	let agentsLoadError = $state<string | null>(null);
	let liveStore: ReturnType<typeof makeChatStore> | null = $state(null);

	async function refreshAgents() {
		try {
			const view = await loadAgents();
			const def =
				view.entries.find((a) => a.id === view.defaultId) ?? view.entries[0] ?? null;
			primary = def;
			if (!primary) {
				agentsLoadError = 'No agents configured on this gateway.';
			}
		} catch (err) {
			agentsLoadError = err instanceof Error ? err.message : String(err);
		}
	}

	$effect(() => {
		refreshAgents();
	});

	// (Re)create the chat store when the primary agent resolves.
	// Cleanup disposes the prior subscribe so a reload or
	// re-resolve doesn't leak.
	$effect(() => {
		const id = primary?.id;
		if (!id) return;
		const store = makeChatStore(`agent:${id}:${LIVE_CONV}`);
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
	const composerModel = $derived.by(() => {
		const s = liveStore;
		return isLive && s ? s.model : null;
	});
	const onModelChange = $derived.by(() => {
		const s = liveStore;
		if (!isLive || !s) return undefined;
		return (modelId: string) => s.setModel(modelId);
	});
	const defaultModelLabel = $derived(
		primary?.primaryModelName || primary?.primaryModel || null
	);

	function selectChannel(id: string) {
		activeId = id;
		if (chrome.isNarrow) chrome.railOpen = false;
	}
</script>

<ChannelRail
	{channels}
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
	model={composerModel}
	{onModelChange}
	{defaultModelLabel}
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
