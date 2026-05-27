<script lang="ts">
	import { create } from '@bufbuild/protobuf';
	import { navTabs } from '$lib/data/sections';
	import { getInfraClient } from '$lib/gateway/connect';
	import { EmptySchema } from '$lib/gateway/gen/talon/v1/common_pb.js';

	let { current = 'chat' }: { current?: string } = $props();

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

	type HealthState = {
		ok: boolean;
		server: string;
		version: string;
		uptimeMs: bigint;
		error: string | null;
	};

	let health = $state<HealthState>({
		ok: false,
		server: 'gateway',
		version: '',
		uptimeMs: 0n,
		error: null,
	});
	let healthBusy = false;

	const healthTone = $derived(health.error ? 'bad' : health.ok ? 'good' : 'warn');
	const healthLabel = $derived(health.error ? 'offline' : health.ok ? 'online' : 'degraded');
	const serverLabel = $derived(health.server || 'gateway');
	const versionLabel = $derived(health.version || 'dev');
	const uptimeLabel = $derived(formatUptime(health.uptimeMs));

	$effect(() => {
		refreshHealth();
		const id = setInterval(refreshHealth, 15_000);
		return () => clearInterval(id);
	});

	async function refreshHealth() {
		if (healthBusy) return;
		healthBusy = true;
		try {
			const res = await getInfraClient().health(create(EmptySchema));
			health = {
				ok: res.ok,
				server: res.server,
				version: res.version,
				uptimeMs: res.uptimeMs,
				error: null,
			};
		} catch (err) {
			health = {
				...health,
				ok: false,
				error: shortError(err),
			};
		} finally {
			healthBusy = false;
		}
	}

	function shortError(err: unknown): string {
		const msg = err instanceof Error ? err.message : String(err);
		return msg.replace(/\s+/g, ' ').slice(0, 120);
	}

	function formatUptime(ms: bigint): string {
		if (ms <= 0n) return '0s';
		const total = Number(ms / 1000n);
		const days = Math.floor(total / 86_400);
		const hours = Math.floor((total % 86_400) / 3_600);
		const minutes = Math.floor((total % 3_600) / 60);
		const seconds = total % 60;
		if (days > 0) return `${days}d ${hours}h`;
		if (hours > 0) return `${hours}h ${minutes}m`;
		if (minutes > 0) return `${minutes}m ${seconds}s`;
		return `${seconds}s`;
	}
</script>

<footer
	class="bar"
	aria-label="Gateway {healthLabel} at {host}"
	title={health.error ?? `${serverLabel} ${versionLabel} uptime ${uptimeLabel}`}
>
	<div class="status-cells">
		<div class="cell gateway">
			<span class="t-label">gw</span>
			<span class="status-dot tone-{healthTone}" aria-hidden="true">●</span>
			<span class="t-mono host">{host}</span>
			<span class="t-mono state">{healthLabel}</span>
		</div>

		<div class="cell hide-sm">
			<span class="t-label">server</span>
			<span class="t-mono">{serverLabel}</span>
			<span class="t-mono muted">{versionLabel}</span>
		</div>

		<div class="cell hide-md">
			<span class="t-label">up</span>
			<span class="t-num">{uptimeLabel}</span>
		</div>

		{#if health.error}
			<div class="cell error hide-md">
				<span class="t-mono truncate">{health.error}</span>
			</div>
		{/if}

		<div class="cell spacer"></div>

		<div class="cell" aria-hidden="true">
			<span class="t-num">{now}</span>
		</div>
	</div>

	<nav class="mobile-nav" aria-label="Primary">
		{#each navTabs as tab (tab.key)}
			<a
				href={tab.href}
				class="mobile-link"
				class:active={current === tab.key}
				aria-current={current === tab.key ? 'page' : undefined}
			>
				<span class="mobile-dot" aria-hidden="true"></span>
				<span class="mobile-label">{tab.label}</span>
			</a>
		{/each}
	</nav>
</footer>

<style>
	.bar {
		display: flex;
		align-items: center;
		height: var(--statusbar-h);
		background: var(--canvas);
		color: var(--ink-2);
		font-size: var(--fs-xs);
		overflow: hidden;
		border-top: 1px solid var(--border);
	}
	.status-cells {
		display: flex;
		align-items: center;
		width: 100%;
		height: 100%;
		min-width: 0;
	}
	.cell {
		display: inline-flex;
		align-items: center;
		gap: var(--s-2);
		height: 100%;
		padding: 0 var(--s-3);
		border-right: 1px solid var(--border);
		line-height: 1;
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
	.gateway {
		min-width: 0;
	}
	.host {
		min-width: 0;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.status-dot {
		font-size: 10px;
		line-height: 1;
	}
	.tone-good {
		color: var(--good);
	}
	.tone-warn {
		color: var(--warn);
	}
	.tone-bad,
	.error {
		color: var(--err);
	}
	.state {
		color: var(--ink-3);
	}
	.truncate {
		display: inline-block;
		max-width: 34vw;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.mobile-nav {
		display: none;
	}

	@media (max-width: 900px) {
		.hide-md { display: none; }
	}
	@media (max-width: 720px) {
		.bar {
			height: 100%;
			padding: 0;
			font-size: var(--fs-xs);
		}
		.status-cells {
			display: none;
		}
		.mobile-nav {
			display: grid;
			grid-template-columns: repeat(5, minmax(0, 1fr));
			width: 100%;
			height: 100%;
			padding: 4px var(--s-1) calc(4px + env(safe-area-inset-bottom));
			background: var(--canvas);
		}
		.mobile-link {
			display: flex;
			flex-direction: column;
			align-items: center;
			justify-content: center;
			gap: 3px;
			min-width: 0;
			min-height: 48px;
			color: var(--ink-3);
			font-size: var(--fs-xs);
			font-weight: 700;
			text-transform: lowercase;
		}
		.mobile-label {
			max-width: 100%;
			overflow: hidden;
			text-overflow: ellipsis;
			white-space: nowrap;
		}
		.mobile-dot {
			width: 5px;
			height: 5px;
			border-radius: 50%;
			background: transparent;
		}
		.mobile-link.active {
			color: var(--accent);
		}
		.mobile-link.active .mobile-dot {
			background: var(--accent);
		}
	}
</style>
