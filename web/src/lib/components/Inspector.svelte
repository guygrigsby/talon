<script lang="ts">
	import type { Channel, Message } from '$lib/data/channels';
	import { sourceLabel } from '$lib/data/channels';
	import {
		loadSelectableModels,
		modelKey,
		type ModelCost,
		type ModelEntry
	} from '$lib/gateway/models';
	import SourceDot from './SourceDot.svelte';

	let {
		channel,
		messages,
		activeModelId = null,
		activeModelSource = null,
		onClose,
		open = true,
	}: {
		channel: Channel;
		messages: Message[];
		activeModelId?: string | null;
		activeModelSource?: string | null;
		onClose?: () => void;
		open?: boolean;
	} = $props();

	let models = $state<ModelEntry[]>([]);
	let modelsLoading = $state(false);
	let modelsError = $state<string | null>(null);

	$effect(() => {
		let cancelled = false;
		modelsLoading = true;
		modelsError = null;
		loadSelectableModels()
			.then((next) => {
				if (cancelled) return;
				models = next;
			})
			.catch((err) => {
				if (cancelled) return;
				modelsError = err instanceof Error ? err.message : String(err);
			})
			.finally(() => {
				if (!cancelled) modelsLoading = false;
			});
		return () => {
			cancelled = true;
		};
	});

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

	const activeModel = $derived.by(() => {
		const key = activeModelId?.trim();
		if (!key) return null;
		return models.find((m) => modelKey(m) === key) ?? null;
	});

	const activeModelParts = $derived.by(() => splitModelKey(activeModelId));
	const activeCost = $derived(activeModel?.cost ?? null);
	const sessionCost = $derived.by(() => estimateSessionCost(totals.inTok, totals.outTok, activeCost));

	function splitModelKey(key: string | null | undefined): { provider: string; id: string } | null {
		const trimmed = key?.trim();
		if (!trimmed) return null;
		const slash = trimmed.indexOf('/');
		if (slash < 0) return { provider: '', id: trimmed };
		return { provider: trimmed.slice(0, slash), id: trimmed.slice(slash + 1) };
	}

	function formatTokens(value?: number): string {
		if (!value || value <= 0) return '—';
		if (value >= 1_000_000) {
			return `${trimFixed(value / 1_000_000, value % 1_000_000 === 0 ? 0 : 1)}M`;
		}
		if (value >= 1_000) {
			return `${trimFixed(value / 1_000, value % 1_000 === 0 ? 0 : 1)}K`;
		}
		return String(value);
	}

	function formatPrice(value: number | undefined): string {
		if (value == null) return '—';
		if (value === 0) return '$0';
		return `$${trimFixed(value, value < 1 ? 3 : 2)}`;
	}

	function formatUSD(value: number): string {
		if (value === 0) return '$0.00';
		if (value > 0 && value < 0.0001) return '<$0.0001';
		return `$${trimFixed(value, value < 0.01 ? 4 : 2)}`;
	}

	function trimFixed(value: number, digits: number): string {
		return value.toFixed(digits).replace(/\.?0+$/, '');
	}

	function priceSourceLabel(source?: string): string {
		if (source === 'priceUsdPer1M') return 'override';
		if (source === 'builtin') return 'built-in';
		if (source === 'catalog') return 'catalog';
		return 'catalog';
	}

	function hasTokenPrice(cost: ModelCost | null): boolean {
		return cost?.input != null || cost?.output != null;
	}

	function estimateSessionCost(inTok: number, outTok: number, cost: ModelCost | null): number | null {
		if (!hasTokenPrice(cost)) return null;
		return (inTok * (cost?.input ?? 0) + outTok * (cost?.output ?? 0)) / 1_000_000;
	}
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

	<section class="block" aria-labelledby="ins-model">
		<h3 id="ins-model" class="t-label block-title">Model</h3>
		{#if activeModelId}
			<dl class="kv model-kv">
				<dt>active</dt><dd class="t-mono wrap">{activeModelId}</dd>
				<dt>source</dt><dd class="t-mono">{activeModelSource ?? '—'}</dd>
				<dt>provider</dt><dd class="t-mono">{activeModel?.provider ?? activeModelParts?.provider ?? '—'}</dd>
				<dt>id</dt><dd class="t-mono wrap">{activeModel?.id ?? activeModelParts?.id ?? '—'}</dd>
				<dt>name</dt><dd class="wrap">{activeModel?.name ?? '—'}</dd>
				<dt>alias</dt><dd class="t-mono wrap">{activeModel?.alias ?? '—'}</dd>
				<dt>api</dt><dd class="t-mono wrap">{activeModel?.api ?? '—'}</dd>
				<dt>auth</dt>
				<dd class:auth-ok={activeModel?.authOk} class:auth-bad={!!activeModel && !activeModel.authOk}>
					{activeModel ? (activeModel.authOk ? 'ok' : 'missing') : modelsLoading ? 'loading' : 'unknown'}
				</dd>
				<dt>ctx</dt><dd class="t-num">{formatTokens(activeModel?.contextWindow)}</dd>
				<dt>max out</dt><dd class="t-num">{formatTokens(activeModel?.maxTokens)}</dd>
				<dt>input</dt><dd class="wrap">{activeModel?.input?.join(', ') || '—'}</dd>
				<dt>reasoning</dt><dd>{activeModel?.reasoning ? 'yes' : activeModel ? 'no' : '—'}</dd>
			</dl>

			{#if activeCost}
				<div class="pricing">
					<div class="pricing-head">
						<span class="t-label">Pricing</span>
						<span class="t-mono source">{priceSourceLabel(activeCost.source)} · per 1M tok</span>
					</div>
					<dl class="price-grid">
						<dt>input</dt><dd class="t-num">{formatPrice(activeCost.input)}</dd>
						<dt>output</dt><dd class="t-num">{formatPrice(activeCost.output)}</dd>
						<dt>cache read</dt><dd class="t-num">{formatPrice(activeCost.cacheRead)}</dd>
						<dt>cache write</dt><dd class="t-num">{formatPrice(activeCost.cacheWrite)}</dd>
					</dl>
				</div>
			{:else if modelsError}
				<p class="note err t-mono" role="status">{modelsError}</p>
			{:else}
				<p class="note t-mono">{modelsLoading ? 'Loading model details…' : 'No built-in pricing found.'}</p>
			{/if}
		{:else}
			<p class="note t-mono">No active model.</p>
		{/if}
	</section>

	<section class="block" aria-labelledby="ins-session">
		<h3 id="ins-session" class="t-label block-title">Session</h3>
		<dl class="kv">
			<dt>total</dt><dd class="t-num">{totals.inTok + totals.outTok} tok</dd>
			<dt>in</dt><dd class="t-num">{totals.inTok}</dd>
			<dt>out</dt><dd class="t-num">{totals.outTok}</dd>
			{#if sessionCost != null}
				<dt>est cost</dt><dd class="t-num">{formatUSD(sessionCost)}</dd>
			{/if}
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
	.kv dd.wrap {
		display: block;
		min-width: 0;
		overflow-wrap: anywhere;
		line-height: 1.35;
	}
	.model-kv {
		grid-template-columns: 78px 1fr;
	}
	.s-connected { color: var(--good); }
	.s-connecting { color: var(--warn); }
	.s-disconnected { color: var(--ink-3); }
	.s-error { color: var(--err); }
	.auth-ok {
		color: var(--good);
	}
	.auth-bad {
		color: var(--warn);
	}
	.pricing {
		margin-top: var(--s-4);
		padding-top: var(--s-3);
		border-top: 1px solid var(--border);
	}
	.pricing-head {
		display: flex;
		align-items: baseline;
		justify-content: space-between;
		gap: var(--s-2);
		margin-bottom: var(--s-2);
	}
	.pricing-head .source {
		color: var(--ink-3);
		font-size: var(--fs-xs);
		text-align: right;
	}
	.price-grid {
		display: grid;
		grid-template-columns: 1fr auto;
		gap: var(--s-2) var(--s-3);
		margin: 0;
	}
	.price-grid dt {
		font-size: var(--fs-xs);
		color: var(--ink-3);
		text-transform: uppercase;
		letter-spacing: var(--tracking-caps);
		font-weight: 700;
	}
	.price-grid dd {
		margin: 0;
		font-size: var(--fs-sm);
		color: var(--ink);
	}
	.note {
		margin: 0;
		font-size: var(--fs-xs);
		color: var(--ink-3);
		line-height: 1.45;
	}
	.note.err {
		color: var(--err);
	}

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
