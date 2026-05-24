<script lang="ts">
	import type { Channel, Message } from '$lib/data/channels';
	import { sourceLabel } from '$lib/data/channels';
	import SourceDot from './SourceDot.svelte';

	let {
		channel,
		messages,
		onClose,
		open = true,
	}: {
		channel: Channel;
		messages: Message[];
		onClose?: () => void;
		open?: boolean;
	} = $props();

	const totals = $derived.by(() => {
		let inTok = 0;
		let outTok = 0;
		let calls = 0;
		let latency = 0;
		let counted = 0;
		for (const m of messages) {
			if (m.tokens) {
				inTok += m.tokens.in;
				outTok += m.tokens.out;
			}
			if (m.toolCalls) calls += m.toolCalls.length;
			if (m.latencyMs != null) {
				latency += m.latencyMs;
				counted += 1;
			}
		}
		return {
			inTok,
			outTok,
			calls,
			avgLatency: counted ? Math.round(latency / counted) : 0,
		};
	});

	const lastAssistant = $derived(
		[...messages].reverse().find((m) => m.role === 'assistant'),
	);
</script>

<aside class="ins" class:is-open={open} aria-label="Channel inspector" aria-hidden={!open}>
	<header class="ins-head">
		<h2 class="t-label">Inspector</h2>
		<button type="button" class="x" onclick={() => onClose?.()} aria-label="Close inspector">
			<svg viewBox="0 0 24 24" width="18" height="18" aria-hidden="true" focusable="false">
				<path d="M6 6 L 18 18 M 18 6 L 6 18" stroke="currentColor" stroke-width="2" fill="none" />
			</svg>
		</button>
	</header>

	<section class="block" aria-labelledby="ins-channel">
		<h3 id="ins-channel" class="t-label block-title">Channel</h3>
		<dl class="kv">
			<dt>id</dt><dd class="t-mono">{channel.id}</dd>
			<dt>source</dt>
			<dd>
				<SourceDot source={channel.source} status={channel.status} />
				<span class="t-mono">{sourceLabel[channel.source]}</span>
			</dd>
			<dt>peer</dt><dd class="t-mono">{channel.peer ?? '—'}</dd>
			<dt>status</dt><dd class="t-mono s-{channel.status}">{channel.status}</dd>
		</dl>
	</section>

	<section class="block" aria-labelledby="ins-session">
		<h3 id="ins-session" class="t-label block-title">Session</h3>
		<dl class="kv">
			<dt>total</dt><dd class="t-num">{totals.inTok + totals.outTok} tok</dd>
			<dt>in</dt><dd class="t-num">{totals.inTok}</dd>
			<dt>out</dt><dd class="t-num">{totals.outTok}</dd>
			<dt>tool calls</dt><dd class="t-num">{totals.calls}</dd>
			<dt>avg latency</dt><dd class="t-num">{totals.avgLatency}ms</dd>
		</dl>
	</section>

	{#if lastAssistant?.toolCalls?.length}
		<section class="block" aria-labelledby="ins-trace">
			<h3 id="ins-trace" class="t-label block-title">Last tool trace</h3>
			<ol class="trace">
				{#each lastAssistant.toolCalls as tc, i (i)}
					<li>
						<div class="trace-head">
							<span class="t-num idx" aria-hidden="true">{String(i + 1).padStart(2, '0')}</span>
							<code class="t-mono name">{tc.name}</code>
							{#if tc.durationMs != null}
								<span class="t-num dur">{tc.durationMs}ms</span>
							{/if}
						</div>
						<pre class="t-mono args">{JSON.stringify(tc.args, null, 2)}</pre>
						{#if tc.result}
							<div class="trace-result">
								<span aria-hidden="true">→</span>
								<span class="t-mono">{tc.result}</span>
							</div>
						{/if}
					</li>
				{/each}
			</ol>
		</section>
	{/if}
</aside>

<style>
	.ins {
		display: flex;
		flex-direction: column;
		width: var(--inspector-w);
		background: var(--canvas);
		color: var(--ink);
		border-left: 1px solid var(--border);
		overflow-y: auto;
	}
	@media (max-width: 720px) {
		.ins {
			position: fixed;
			right: 0;
			top: var(--topbar-h);
			bottom: var(--statusbar-h);
			width: min(86vw, 360px);
			z-index: 50;
			transform: translateX(100%);
			transition: transform 180ms ease-out;
			box-shadow: var(--shadow-pop);
		}
		.ins.is-open {
			transform: translateX(0);
		}
	}
	@media (prefers-reduced-motion: reduce) {
		.ins {
			transition: none;
		}
	}
	.ins-head {
		display: flex;
		justify-content: space-between;
		align-items: center;
		padding: var(--s-3) var(--s-4);
		border-bottom: 1px solid var(--border);
		min-height: var(--tap);
	}
	.x {
		display: inline-flex;
		align-items: center;
		justify-content: center;
		width: var(--tap);
		height: var(--tap);
		border-radius: var(--radius-sm);
		color: var(--ink-3);
	}
	.x:hover {
		color: var(--ink);
		background: var(--surface-2);
	}

	.block {
		padding: var(--s-4);
		border-bottom: 1px solid var(--border);
	}
	.block-title {
		margin: 0 0 var(--s-3);
	}

	.kv {
		display: grid;
		grid-template-columns: 92px 1fr;
		gap: var(--s-2) var(--s-3);
		margin: 0;
	}
	.kv dt {
		font-size: var(--fs-xs);
		color: var(--ink-3);
		text-transform: uppercase;
		letter-spacing: var(--tracking-caps);
		font-weight: 700;
	}
	.kv dd {
		margin: 0;
		font-size: var(--fs-sm);
		color: var(--ink);
		display: inline-flex;
		gap: 6px;
		align-items: center;
	}
	.s-connected { color: var(--good); }
	.s-connecting { color: var(--warn); }
	.s-disconnected { color: var(--ink-3); }
	.s-error { color: var(--err); }

	.trace {
		list-style: none;
		margin: 0;
		padding: 0;
		display: flex;
		flex-direction: column;
		gap: var(--s-3);
	}
	.trace-head {
		display: flex;
		align-items: baseline;
		gap: var(--s-2);
	}
	.idx {
		color: var(--ink-3);
	}
	.name {
		color: var(--ink);
		font-weight: 700;
		font-size: var(--fs-sm);
	}
	.dur {
		margin-left: auto;
		font-size: var(--fs-xs);
		color: var(--ink-3);
	}
	.args {
		margin: 6px 0 0;
		padding: var(--s-2);
		background: var(--surface);
		border: 1px solid var(--border);
		border-radius: var(--radius-sm);
		font-size: var(--fs-xs);
		line-height: 1.5;
		color: var(--ink-2);
		white-space: pre-wrap;
		max-height: 140px;
		overflow-y: auto;
	}
	.trace-result {
		margin-top: 4px;
		font-size: var(--fs-xs);
		color: var(--accent-strong);
	}
</style>
