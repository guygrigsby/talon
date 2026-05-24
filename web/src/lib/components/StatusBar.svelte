<script lang="ts">
	import { channels, bySource, sourceLabel } from '$lib/data/channels';
	import SourceDot from './SourceDot.svelte';

	const grouped = bySource(channels);

	const fmt = () =>
		new Date().toLocaleTimeString('en-GB', { hour: '2-digit', minute: '2-digit', second: '2-digit' });
	let now = $state(fmt());
	$effect(() => {
		const id = setInterval(() => (now = fmt()), 1000);
		return () => clearInterval(id);
	});
</script>

<footer class="bar" aria-hidden="true">
	<div class="cell">
		<span class="t-label">gw</span>
		<span class="t-mono live">● ws://127.0.0.1:18789</span>
	</div>

	<div class="cell">
		<span class="t-label">channels</span>
		<span class="pips">
			{#each [...grouped.entries()] as [source, list] (source)}
				<span class="pip" title="{sourceLabel[source]} · {list.length}">
					<SourceDot {source} status={list[0].status} size={6} />
					<span class="t-num">{list.length}</span>
				</span>
			{/each}
		</span>
	</div>

	<div class="cell hide-sm">
		<span class="t-label">model</span>
		<span class="t-mono">sonnet-4.6</span>
		<span class="t-num muted">ctx 12.4%</span>
	</div>

	<div class="cell hide-md">
		<span class="t-label">last rpc</span>
		<span class="t-mono">chat.send</span>
		<span class="t-num muted">142ms</span>
	</div>

	<div class="cell spacer"></div>

	<div class="cell hide-sm">
		<span class="t-mono muted">v0.1.0-dev</span>
	</div>

	<div class="cell">
		<span class="t-num">{now}</span>
	</div>
</footer>

<style>
	.bar {
		display: flex;
		align-items: stretch;
		height: var(--statusbar-h);
		background: var(--canvas);
		color: var(--ink-2);
		font-size: var(--fs-xs);
		overflow: hidden;
		border-top: 1px solid var(--border);
	}
	.cell {
		display: inline-flex;
		align-items: center;
		gap: var(--s-2);
		padding: 0 var(--s-3);
		border-right: 1px solid var(--border);
	}
	.cell.spacer {
		flex: 1;
		border-right: 0;
		padding: 0;
	}
	.cell:last-child {
		border-right: 0;
	}
	.muted {
		color: var(--ink-3);
	}
	.live {
		color: var(--good);
	}
	.pips {
		display: inline-flex;
		gap: var(--s-2);
		align-items: center;
	}
	.pip {
		display: inline-flex;
		align-items: center;
		gap: 4px;
		color: var(--ink-2);
	}

	@media (max-width: 720px) {
		.hide-sm { display: none; }
	}
	@media (max-width: 960px) {
		.hide-md { display: none; }
	}
</style>
