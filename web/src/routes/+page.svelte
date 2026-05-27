<script lang="ts">
	import Transcript from '$lib/components/Transcript.svelte';
	import Inspector from '$lib/components/Inspector.svelte';
	import { channels as staticChannels, type Channel } from '$lib/data/channels';
	import { chrome } from '$lib/state/chrome.svelte';
	import { makeChatStore } from '$lib/gateway/chatStore.svelte';
	import { loadAgents, type AgentEntry } from '$lib/gateway/agents';
	import { loadConfiguredChannels } from '$lib/gateway/channels';

	const LIVE_CHANNEL_ID = 'web-here';
	const LIVE_CONV = 'web';
	const channel: Channel =
		staticChannels.find((c) => c.id === LIVE_CHANNEL_ID) ?? {
			id: LIVE_CHANNEL_ID,
			source: 'web',
			name: 'this session',
			peer: 'localhost',
			unread: 0,
			lastActive: 'now',
			status: 'connected',
		};

	// Source summary entries: the live web session plus configured
	// external bridges (telegram, bluebubbles, ...). Chat stays tied
	// to the primary web session; these rows are status, not tabs.
	let liveChannels = $state<Channel[]>([]);
	const sourceChannels = $derived<Channel[]>([channel, ...liveChannels]);

	async function refreshChannels() {
		try {
			const next = await loadConfiguredChannels();
			console.info('[talon] configured channels:', next);
			liveChannels = next;
		} catch (err) {
			// Config read failures are non-fatal; the live web session
			// remains available even if configured bridges cannot be
			// enumerated. Keep the console breadcrumb for local debugging.
			console.warn('[talon] loadConfiguredChannels failed:', err);
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

	const stream = $derived.by(() => {
		const s = liveStore;
		return s?.messages ?? [];
	});
	const composerStatus = $derived.by(() => {
		const s = liveStore;
		if (s) return s.status;
		return agentsLoadError ? ('error' as const) : ('loading' as const);
	});
	const composerError = $derived.by(() => {
		const s = liveStore;
		return s?.errorMessage ?? agentsLoadError;
	});
	const onSend = $derived.by(() => {
		const s = liveStore;
		if (!s) return undefined;
		return (text: string) => s.send(text);
	});
	const composerModel = $derived.by(() => {
		const s = liveStore;
		return s ? s.model : null;
	});
	const onModelChange = $derived.by(() => {
		const s = liveStore;
		if (!s) return undefined;
		return (modelId: string) => s.setModel(modelId);
	});
	const defaultModelLabel = $derived(
		primary?.primaryModelName || primary?.primaryModel || null
	);
	const activeModelId = $derived.by(() => {
		return composerModel || primary?.primaryModel || null;
	});
	const activeModelSource = $derived.by(() => {
		if (!activeModelId) return null;
		return composerModel ? 'session override' : 'agent default';
	});
</script>

<Transcript
	{channel}
	sources={sourceChannels}
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
	{activeModelId}
	{activeModelSource}
	open={chrome.inspectorOpen}
	onClose={() => (chrome.inspectorOpen = false)}
/>

{#if chrome.isNarrow && chrome.inspectorOpen}
	<button
		type="button"
		class="backdrop"
		aria-label="Close panel"
		onclick={() => chrome.closePanelsOnNarrow()}
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
