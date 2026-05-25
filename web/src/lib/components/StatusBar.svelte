<script lang="ts">
	const fmt = () =>
		new Date().toLocaleTimeString('en-GB', { hour: '2-digit', minute: '2-digit', second: '2-digit' });
	let now = $state(fmt());
	$effect(() => {
		const id = setInterval(() => (now = fmt()), 1000);
		return () => clearInterval(id);
	});

	// host renders as `127.0.0.1:18789` (or wherever the SPA is
	// served from). No scheme prefix — Connect uses HTTP and the
	// hostname is the only useful diagnostic when the user wants
	// to confirm which gateway they're talking to.
	const host = $derived(typeof location === 'undefined' ? '' : location.host);
</script>

<footer class="bar" aria-hidden="true">
	<div class="cell">
		<span class="t-label">gw</span>
		<span class="t-mono live">● {host}</span>
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

	@media (max-width: 720px) {
		.hide-sm { display: none; }
	}
</style>
