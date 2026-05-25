<script lang="ts">
	import type { Channel, Message } from '$lib/data/channels';
	import SourceDot from './SourceDot.svelte';

	let {
		message,
		channel,
	}: { message: Message; channel: Channel } = $props();

	// Tracks which tool-call rows are expanded. Key by index — stable
	// within a message because the order is the model's call order
	// and rows don't get reordered.
	let expanded = $state<Set<number>>(new Set());

	function toggle(i: number) {
		const next = new Set(expanded);
		if (next.has(i)) next.delete(i);
		else next.add(i);
		expanded = next;
	}
</script>

<article class="row r-{message.role}">
	<header class="meta">
		<span class="ts t-mono">{message.ts}</span>
		<span class="dot-wrap" aria-hidden="true">
			<SourceDot source={channel.source} status="connected" size={6} />
		</span>
		<span class="who">{message.author}</span>
		<span class="role">{message.role}</span>
		{#if message.model}
			<span class="sep" aria-hidden="true">·</span>
			<span class="t-mono dim">{message.model}</span>
		{/if}
		{#if message.tokens}
			<span class="sep" aria-hidden="true">·</span>
			<span class="t-num dim">{message.tokens.in}→{message.tokens.out} tok</span>
		{/if}
		{#if message.latencyMs != null}
			<span class="sep" aria-hidden="true">·</span>
			<span class="t-num dim">{message.latencyMs}ms</span>
		{/if}
	</header>

	<div class="text t-body">{message.body}</div>

	{#if message.toolCalls?.length}
		<section class="tools" aria-label="Tool calls">
			<h4 class="tools-head t-label">
				{message.toolCalls.length} tool call{message.toolCalls.length === 1 ? '' : 's'}
			</h4>
			<ol>
				{#each message.toolCalls as tc, i (i)}
					{@const isOpen = expanded.has(i)}
					<li>
						<button
							type="button"
							class="tc-summary"
							aria-expanded={isOpen}
							aria-controls="tc-{message.id}-{i}-detail"
							onclick={() => toggle(i)}
						>
							<span class="tc-caret" aria-hidden="true">{isOpen ? '▾' : '▸'}</span>
							<span class="tc-idx t-num" aria-hidden="true">{String(i + 1).padStart(2, '0')}</span>
							<code class="tc-name t-mono">{tc.name}</code>
							{#if !isOpen}
								<code class="tc-args tc-args-compact t-mono">{JSON.stringify(tc.args)}</code>
								{#if tc.result}
									<span class="tc-arrow" aria-hidden="true">→</span>
									<code class="tc-result tc-result-compact t-mono">{tc.result}</code>
								{/if}
							{/if}
							{#if tc.durationMs != null}
								<span class="tc-dur t-num">{tc.durationMs}ms</span>
							{/if}
						</button>
						{#if isOpen}
							<div
								class="tc-detail"
								id="tc-{message.id}-{i}-detail"
								role="region"
								aria-label="{tc.name} details"
							>
								<div class="tc-block">
									<span class="t-label tc-block-label">args</span>
									<pre class="tc-pre t-mono">{JSON.stringify(tc.args, null, 2)}</pre>
								</div>
								{#if tc.result}
									<div class="tc-block">
										<span class="t-label tc-block-label">result</span>
										<pre class="tc-pre t-mono">{tc.result}</pre>
									</div>
								{/if}
							</div>
						{/if}
					</li>
				{/each}
			</ol>
		</section>
	{/if}
</article>

<style>
	.row {
		padding: var(--s-4) 0;
		border-bottom: 1px solid var(--border);
	}
	.row:last-child {
		border-bottom: 0;
	}

	.meta {
		display: flex;
		flex-wrap: wrap;
		align-items: baseline;
		gap: 7px;
		font-size: var(--fs-xs);
		color: var(--ink-3);
		margin-bottom: var(--s-2);
		line-height: var(--lh-snug);
	}
	.ts {
		color: var(--ink-3);
	}
	.dot-wrap {
		display: inline-flex;
		align-items: center;
	}
	.who {
		font-weight: 700;
		font-size: var(--fs-sm);
		color: var(--ink);
	}
	.role {
		font-size: var(--fs-xs);
		text-transform: uppercase;
		letter-spacing: var(--tracking-caps);
		font-weight: 700;
		color: var(--ink-3);
	}
	.r-assistant .role {
		color: var(--accent);
	}
	.dim,
	.sep {
		color: var(--ink-3);
	}

	.text {
		font-size: var(--fs-md);
		line-height: var(--lh-body);
		color: var(--ink);
		white-space: pre-wrap;
		word-wrap: break-word;
		max-width: 70ch;
	}

	.tools {
		margin-top: var(--s-3);
		padding: var(--s-3);
		border: 1px solid var(--border);
		border-radius: var(--radius);
	}
	.tools-head {
		margin: 0 0 var(--s-2);
	}
	.tools ol {
		list-style: none;
		margin: 0;
		padding: 0;
		display: flex;
		flex-direction: column;
		gap: 2px;
	}
	.tools li {
		font-family: var(--ff-mono);
		font-size: var(--fs-sm);
		color: var(--ink-2);
		line-height: var(--lh-snug);
		padding: 2px 0;
	}
	.tc-summary {
		display: flex;
		gap: var(--s-2);
		align-items: baseline;
		width: 100%;
		background: transparent;
		border: 0;
		padding: 4px 0;
		text-align: left;
		font: inherit;
		color: inherit;
		cursor: pointer;
		min-height: var(--tap, 32px);
	}
	.tc-summary:hover {
		color: var(--ink);
	}
	.tc-summary:focus-visible {
		outline: 2px solid var(--accent);
		outline-offset: 2px;
		border-radius: var(--radius);
	}
	.tc-caret {
		width: 12px;
		color: var(--ink-3);
		flex-shrink: 0;
	}
	.tc-args-compact,
	.tc-result-compact {
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
		min-width: 0;
	}
	.tc-detail {
		margin: var(--s-2) 0 var(--s-3) calc(12px + var(--s-2) + 24px);
		display: flex;
		flex-direction: column;
		gap: var(--s-3);
	}
	.tc-block {
		display: flex;
		flex-direction: column;
		gap: 4px;
	}
	.tc-block-label {
		color: var(--ink-3);
	}
	.tc-pre {
		margin: 0;
		padding: var(--s-2) var(--s-3);
		background: var(--canvas);
		border: 1px solid var(--border);
		border-radius: var(--radius);
		font-size: var(--fs-xs);
		color: var(--ink);
		white-space: pre-wrap;
		word-break: break-word;
		max-height: 320px;
		overflow: auto;
	}
	.tc-idx {
		color: var(--ink-3);
	}
	.tc-name {
		color: var(--ink);
		font-weight: 700;
	}
	.tc-args {
		color: var(--ink-3);
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
		max-width: 380px;
	}
	@media (max-width: 720px) {
		.tc-args {
			max-width: 50vw;
		}
		.tools li {
			flex-wrap: wrap;
		}
	}
	.tc-arrow {
		color: var(--ink-3);
	}
	.tc-result {
		color: var(--accent-strong);
	}
	.tc-dur {
		margin-left: auto;
		color: var(--ink-3);
		font-size: var(--fs-xs);
	}
</style>
