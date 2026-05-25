<script lang="ts">
	import type { Channel, Message } from '$lib/data/channels';
	import { sourceLabel } from '$lib/data/channels';
	import MessageRow from './MessageRow.svelte';
	import ModelPicker from './ModelPicker.svelte';
	import SourceDot from './SourceDot.svelte';

	let {
		channel,
		messages,
		onToggleInspector,
		inspectorOpen = true,
		onSend,
		status = 'idle',
		errorMessage = null,
		model = null,
		onModelChange,
	}: {
		channel: Channel;
		messages: Message[];
		onToggleInspector?: () => void;
		inspectorOpen?: boolean;
		onSend?: (text: string) => void | Promise<void>;
		status?: 'idle' | 'loading' | 'streaming' | 'error';
		errorMessage?: string | null;
		model?: string | null;
		onModelChange?: (modelId: string) => void;
	} = $props();

	let draft = $state('');
	let textarea: HTMLTextAreaElement | undefined;

	const wired = $derived(onSend != null);
	const busy = $derived(status === 'streaming' || status === 'loading');

	async function submit() {
		if (!onSend) return;
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
		<div class="ops">
			{#if onModelChange}
				<ModelPicker value={model ?? ''} onChange={onModelChange} disabled={!wired} />
			{/if}
			<button
				type="button"
				class="op"
				onclick={() => onToggleInspector?.()}
				aria-pressed={inspectorOpen}
			>
				Inspector
			</button>
		</div>
	</header>

	<div class="stream" tabindex="-1">
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
				disabled={!wired}
				placeholder={wired
					? 'Write a message…  (⇧⏎ newline · ⏎ send)'
					: 'Composer disabled for this channel.'}
				aria-describedby="composer-help"
			></textarea>
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
		align-items: center;
		justify-content: space-between;
		gap: var(--s-3);
		padding: var(--s-3) var(--s-6);
		border-bottom: 1px solid var(--border);
		min-height: var(--tap);
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

	.ops {
		display: inline-flex;
		gap: var(--s-2);
	}
	.op {
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
		align-items: flex-start;
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
	}
	.input::placeholder {
		color: var(--ink-3);
	}
	.input:disabled {
		opacity: 0.6;
		cursor: not-allowed;
	}
	.err {
		margin-top: var(--s-2);
		font-size: var(--fs-xs);
		color: var(--accent-strong, var(--ink));
	}

	@media (max-width: 720px) {
		.head {
			padding: var(--s-3) var(--s-4);
		}
		.stream {
			padding: 0 var(--s-4);
		}
		.composer {
			padding: var(--s-3) var(--s-4);
		}
		.input {
			min-height: 48px;
		}
	}
</style>
