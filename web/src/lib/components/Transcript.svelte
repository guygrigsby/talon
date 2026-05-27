<script lang="ts">
	import type { Channel, Message } from '$lib/data/channels';
	import { sourceLabel } from '$lib/data/channels';
	import MessageRow from './MessageRow.svelte';
	import ModelPicker from './ModelPicker.svelte';
	import SourceDot from './SourceDot.svelte';

	let {
		channel,
		sources = [],
		messages,
		onToggleInspector,
		inspectorOpen = true,
		onSend,
		status = 'idle',
		errorMessage = null,
		model = null,
		onModelChange,
		defaultModelLabel = null,
	}: {
		channel: Channel;
		sources?: Channel[];
		messages: Message[];
		onToggleInspector?: () => void;
		inspectorOpen?: boolean;
		onSend?: (text: string) => void | Promise<void>;
		status?: 'idle' | 'loading' | 'streaming' | 'error';
		errorMessage?: string | null;
		model?: string | null;
		onModelChange?: (modelId: string) => void;
		// Resolved primary-agent default model name, used by the
		// model picker's "agent default" option label.
		defaultModelLabel?: string | null;
	} = $props();

	// Auto-pin the stream to the bottom on new content. Track the
	// last user-scroll position so a user mid-scrollback doesn't get
	// yanked when a delta lands. Threshold of 80px gives a little
	// slack — if you're within a hair of the bottom we assume you
	// want to keep tracking.
	let stream: HTMLDivElement | undefined;
	let pinToBottom = $state(true);
	const messageCount = $derived(messages.length);
	const lastBodyLen = $derived(messages[messageCount - 1]?.body.length ?? 0);

	$effect(() => {
		// Re-runs on any change in count or growing body. pinToBottom
		// is updated in the onscroll handler below; honor it here.
		void messageCount;
		void lastBodyLen;
		if (!pinToBottom || !stream) return;
		// scrollTop = scrollHeight scrolls to the very bottom; queue
		// to next animation frame so the DOM has settled after the
		// reactive update.
		requestAnimationFrame(() => {
			if (stream) stream.scrollTop = stream.scrollHeight;
		});
	});

	function onStreamScroll() {
		if (!stream) return;
		const distance = stream.scrollHeight - stream.scrollTop - stream.clientHeight;
		pinToBottom = distance < 80;
	}

	let draft = $state('');
	let textarea: HTMLTextAreaElement | undefined;

	const wired = $derived(onSend != null);
	const busy = $derived(status === 'streaming' || status === 'loading');
	// Disable only when the composer is permanently dead for this
	// channel. While the gateway is still wiring up (status=loading)
	// keep it enabled so the user can reload and start typing — the
	// draft buffers until onSend lands.
	const composerDisabled = $derived(!wired && status !== 'loading');
	const canSubmit = $derived(wired && !busy && draft.trim().length > 0);

	function sourceChipLabel(src: Channel): string {
		if (src.source === 'web') return 'web';
		return src.name || sourceLabel[src.source].toLowerCase();
	}

	function sourceStateLabel(src: Channel): string {
		if (src.peer === 'unconfigured') return 'unconfigured';
		return src.status;
	}

	function sourceTitle(src: Channel): string {
		return [sourceLabel[src.source].toLowerCase(), src.name, src.status, src.peer]
			.filter(Boolean)
			.join(' · ');
	}

	// Action: focus on mount. Runs once, deterministically, when the
	// element is in the DOM. rAF lets any post-hydration focus reset
	// (SvelteKit a11y, scroll restore) settle before we claim focus.
	function focusOnMount(node: HTMLTextAreaElement) {
		requestAnimationFrame(() => node.focus());
	}

	async function submit() {
		if (!onSend || !canSubmit) return;
		const text = draft.trim();
		if (!text) return;
		draft = '';
		try {
			await onSend(text);
		} finally {
			// Focus stays in the composer so the next message
			// flows without re-clicking.
			textarea?.focus();
		}
	}

	function onComposerKeydown(e: KeyboardEvent) {
		// ⏎ sends, ⇧⏎ inserts a newline. Matches the placeholder
		// hint + every chat app the user has muscle memory for.
		if (e.key === 'Enter' && !e.shiftKey && !e.metaKey && !e.ctrlKey) {
			e.preventDefault();
			submit();
		}
	}
</script>

<section class="transcript" aria-labelledby="ch-title">
	<header class="head">
		<div class="head-main">
			<div class="crumbs">
				<SourceDot source={channel.source} status={channel.status} size={8} />
				<h1 id="ch-title" class="crumb-name">{channel.name}</h1>
				{#if channel.peer}
					<span class="t-mono crumb-peer">{channel.peer}</span>
				{/if}
				<span class="t-mono crumb-meta">
					{messages.length} turns · {channel.status} · last {channel.lastActive}
				</span>
			</div>

			{#if sources.length}
				<div class="sources" aria-label="Configured sources">
					<span class="t-label source-label">Sources</span>
					{#each sources as src (src.id)}
						<span
							class="source-chip s-{src.status}"
							title={sourceTitle(src)}
							aria-label={sourceTitle(src)}
						>
							<SourceDot source={src.source} status={src.status} size={7} />
							<span class="source-name">{sourceChipLabel(src)}</span>
							<span class="t-mono source-state">{sourceStateLabel(src)}</span>
						</span>
					{/each}
				</div>
			{/if}
		</div>
		<div class="ops">
			{#if onModelChange}
				<ModelPicker
					value={model ?? ''}
					onChange={onModelChange}
					disabled={!wired}
					defaultLabel={defaultModelLabel}
				/>
			{/if}
			<button
				type="button"
				class="op"
				onclick={() => onToggleInspector?.()}
				aria-pressed={inspectorOpen}
				aria-label="Open details"
			>
				<svg class="op-icon" viewBox="0 0 24 24" width="18" height="18" aria-hidden="true">
					<circle cx="12" cy="12" r="8" stroke="currentColor" stroke-width="2" fill="none" />
					<path d="M12 11 V 16" stroke="currentColor" stroke-width="2" stroke-linecap="round" />
					<circle cx="12" cy="8" r="1" fill="currentColor" />
				</svg>
				<span class="op-label">Details</span>
			</button>
		</div>
	</header>

	<div class="stream" tabindex="-1" bind:this={stream} onscroll={onStreamScroll}>
		{#each messages as msg (msg.id)}
			<MessageRow message={msg} {channel} />
		{/each}
	</div>

	<form
		class="composer"
		onsubmit={(e) => {
			e.preventDefault();
			submit();
		}}
	>
		<label class="composer-label" for="composer-input">
			<span class="t-label">Send as</span>
			<span class="via t-mono">
				{wired ? `web · ${channel.name}` : 'web · this session (not wired)'}
				{#if status === 'streaming'}
					· streaming
				{:else if status === 'loading'}
					· loading
				{:else if status === 'error'}
					· error
				{/if}
			</span>
		</label>
		<div class="input-wrap">
			<textarea
				id="composer-input"
				class="input"
				rows="2"
				bind:this={textarea}
				bind:value={draft}
				onkeydown={onComposerKeydown}
				disabled={composerDisabled}
				use:focusOnMount
				placeholder={wired
					? 'Message Talon'
					: status === 'loading'
						? 'Connecting…'
						: 'Composer disabled for this channel.'}
				aria-describedby="composer-help"
			></textarea>
			<button type="submit" class="send" disabled={!canSubmit} aria-label="Send message">
				<svg viewBox="0 0 24 24" width="18" height="18" aria-hidden="true">
					<path d="M12 5 V 19 M 12 5 L 6 11 M 12 5 L 18 11" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" fill="none" />
				</svg>
			</button>
		</div>
		<span id="composer-help" class="sr-only">
			Compose a message to {channel.name} on {sourceLabel[channel.source]}.
			{wired ? 'Press Enter to send, Shift+Enter for newline.' : 'Not yet wired.'}
		</span>
		{#if errorMessage}
			<div class="err t-mono" role="status">{errorMessage}</div>
		{/if}
	</form>
</section>

<style>
	.transcript {
		display: flex;
		flex-direction: column;
		min-width: 0;
		min-height: 0;
		flex: 1;
		background: var(--surface);
		color: var(--ink);
	}

	.head {
		display: flex;
		align-items: flex-start;
		justify-content: space-between;
		gap: var(--s-3);
		padding: var(--s-3) var(--s-6);
		border-bottom: 1px solid var(--border);
		min-height: var(--tap);
	}
	.head-main {
		display: flex;
		flex-direction: column;
		gap: var(--s-2);
		min-width: 0;
	}
	.crumbs {
		display: inline-flex;
		align-items: baseline;
		gap: var(--s-2);
		min-width: 0;
		flex-wrap: wrap;
	}
	.crumbs :global(.dot) {
		align-self: center;
	}
	.crumb-name {
		font-weight: 700;
		font-size: var(--fs-md);
		color: var(--ink);
	}
	.crumb-peer {
		color: var(--ink-2);
		font-size: var(--fs-xs);
	}
	.crumb-meta {
		color: var(--ink-3);
		font-size: var(--fs-xs);
	}
	.sources {
		display: flex;
		align-items: center;
		flex-wrap: wrap;
		gap: var(--s-2);
		min-width: 0;
	}
	.source-label {
		margin-right: 2px;
	}
	.source-chip {
		display: inline-flex;
		align-items: center;
		gap: 6px;
		min-width: 0;
		max-width: 190px;
		min-height: 24px;
		padding: 0 var(--s-2);
		border: 1px solid var(--border);
		border-radius: var(--radius-sm);
		background: var(--canvas);
		color: var(--ink-2);
		font-size: var(--fs-xs);
	}
	.source-name {
		min-width: 0;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.source-state {
		color: var(--ink-3);
	}
	.source-chip.s-connected .source-state {
		color: var(--good);
	}
	.source-chip.s-connecting .source-state {
		color: var(--warn);
	}
	.source-chip.s-error .source-state {
		color: var(--err);
	}

	.ops {
		display: inline-flex;
		align-items: center;
		gap: var(--s-2);
		flex-shrink: 0;
	}
	.op {
		display: inline-flex;
		align-items: center;
		justify-content: center;
		gap: var(--s-2);
		color: var(--ink-2);
		padding: 0 var(--s-3);
		min-height: 32px;
		font-size: var(--fs-sm);
		font-weight: 700;
		border: 1px solid var(--border);
		border-radius: var(--radius);
		background: var(--surface);
	}
	.op:hover {
		color: var(--ink);
		border-color: var(--border-strong);
	}
	.op[aria-pressed='true'] {
		color: var(--accent-strong);
		border-color: var(--accent-edge);
		background: var(--accent-tint);
	}
	.op-icon {
		flex-shrink: 0;
	}

	.stream {
		flex: 1;
		overflow-y: auto;
		padding: 0 var(--s-6);
	}
	.stream:focus-visible {
		outline: 2px solid var(--accent);
		outline-offset: -3px;
		box-shadow: none;
	}

	.composer {
		border-top: 1px solid var(--border);
		background: var(--canvas);
		padding: var(--s-3) var(--s-6);
	}
	.composer-label {
		display: inline-flex;
		gap: var(--s-2);
		align-items: baseline;
		margin-bottom: var(--s-2);
		cursor: text;
	}
	.composer-label .via {
		font-size: var(--fs-xs);
		color: var(--ink-2);
	}
	.input-wrap {
		display: flex;
		align-items: flex-end;
		gap: var(--s-2);
		background: var(--surface);
		border: 1px solid var(--border-strong);
		border-radius: var(--radius);
		padding: var(--s-2) var(--s-3);
	}
	.input-wrap:focus-within {
		border-color: var(--accent);
		box-shadow: 0 0 0 3px var(--accent-tint);
	}
	.input {
		flex: 1;
		background: transparent;
		border: 0;
		outline: 0;
		resize: vertical;
		font-family: var(--ff-body);
		font-size: var(--fs-md);
		line-height: var(--lh-body);
		color: var(--ink);
		min-height: 44px;
		max-height: 180px;
	}
	.input::placeholder {
		color: var(--ink-3);
	}
	.input:disabled {
		opacity: 0.6;
		cursor: not-allowed;
	}
	.send {
		display: inline-flex;
		align-items: center;
		justify-content: center;
		width: 34px;
		height: 34px;
		margin-bottom: 2px;
		border-radius: var(--radius);
		background: var(--accent);
		color: var(--on-accent);
		flex-shrink: 0;
	}
	.send:disabled {
		background: var(--surface-3);
		color: var(--ink-3);
		cursor: not-allowed;
	}
	.send:not(:disabled):hover {
		background: var(--accent-strong);
	}
	.err {
		margin-top: var(--s-2);
		font-size: var(--fs-xs);
		color: var(--accent-strong, var(--ink));
	}

	@media (max-width: 720px) {
		.head {
			align-items: center;
			flex-direction: row;
			padding: var(--s-2) var(--s-3);
			gap: var(--s-2);
			min-height: 48px;
		}
		.head-main {
			display: none;
		}
		.ops {
			width: 100%;
			flex: 1;
			justify-content: space-between;
			min-width: 0;
		}
		.ops :global(.model-picker) {
			flex: 1;
			min-width: 0;
			justify-content: flex-start;
		}
		.ops :global(.model-picker .t-label) {
			display: none;
		}
		.ops :global(.model-picker select) {
			width: min(100%, 240px);
			min-height: 36px;
		}
		.op {
			width: 36px;
			min-height: 36px;
			padding: 0;
			border-color: transparent;
			background: transparent;
		}
		.op-label {
			display: none;
		}
		.stream {
			padding: var(--s-1) var(--s-3) 0;
		}
		.composer {
			padding: var(--s-2) var(--s-3) calc(var(--s-2) + env(safe-area-inset-bottom));
		}
		.composer-label {
			display: none;
		}
		.input-wrap {
			border-radius: 18px;
			padding: 7px 7px 7px var(--s-3);
		}
		.input {
			min-height: 28px;
			max-height: 120px;
			resize: none;
			line-height: var(--lh-snug);
		}
		.send {
			width: 32px;
			height: 32px;
			border-radius: 50%;
			margin-bottom: 0;
		}
	}
</style>
