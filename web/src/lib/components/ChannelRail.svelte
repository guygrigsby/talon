<script lang="ts">
	import { bySource, sourceLabel, type Channel } from '$lib/data/channels';
	import SourceDot from './SourceDot.svelte';

	let {
		channels = [],
		activeId = 'web-here',
		onSelect,
		open = true,
		onClose,
	}: {
		channels?: Channel[];
		activeId?: string;
		onSelect?: (id: string) => void;
		open?: boolean;
		onClose?: () => void;
	} = $props();

	// Derived so the rail re-renders when the parent loads live
	// channels (telegram, bluebubbles, etc.) after the initial
	// mount. The previous static import made these counts frozen.
	const grouped = $derived(bySource(channels));
	const totalConnected = $derived(channels.filter((c) => c.status === 'connected').length);
	const totalUnread = $derived(channels.reduce((n, c) => n + c.unread, 0));
</script>

<aside
	id="channel-rail"
	class="rail"
	class:is-open={open}
	aria-label="Channels"
	aria-hidden={!open}
>
	<header class="rail-head">
		<h2 class="t-label">Channels</h2>
		<div class="counts" aria-label="{totalConnected} live, {totalUnread} unread">
			<span class="num">{totalConnected}</span>
			<span class="num-label">live</span>
			<span class="sep" aria-hidden="true">·</span>
			<span class="num">{totalUnread}</span>
			<span class="num-label">unread</span>
		</div>
		<button
			type="button"
			class="rail-close"
			aria-label="Close channels"
			onclick={() => onClose?.()}
		>
			<svg viewBox="0 0 24 24" width="18" height="18" aria-hidden="true" focusable="false">
				<path d="M6 6 L 18 18 M 18 6 L 6 18" stroke="currentColor" stroke-width="2" fill="none" />
			</svg>
		</button>
	</header>

	<nav class="groups" aria-label="Channel list">
		{#each [...grouped.entries()] as [source, list] (source)}
			<section class="group" aria-labelledby="src-{source}">
				<header class="group-head">
					<h3 id="src-{source}" class="t-label">{sourceLabel[source]}</h3>
					<span class="t-num group-count" aria-hidden="true">{list.length}</span>
				</header>
				<ul>
					{#each list as ch (ch.id)}
						<li>
							<button
								type="button"
								class="row"
								class:active={ch.id === activeId}
								aria-current={ch.id === activeId ? 'true' : undefined}
								onclick={() => onSelect?.(ch.id)}
							>
								<SourceDot source={ch.source} status={ch.status} />
								<span class="name">{ch.name}</span>
								{#if ch.peer}
									<span class="peer t-mono">{ch.peer}</span>
								{/if}
								{#if ch.unread > 0}
									<span class="unread t-num" aria-label="{ch.unread} unread">{ch.unread}</span>
								{:else}
									<span class="last t-mono" aria-hidden="true">{ch.lastActive}</span>
								{/if}
							</button>
						</li>
					{/each}
				</ul>
			</section>
		{/each}
	</nav>

	<footer class="rail-foot">
		<button type="button" class="add" aria-disabled="true" title="not yet wired">
			<span class="plus" aria-hidden="true">+</span>
			<span>Pair channel</span>
		</button>
	</footer>
</aside>

<style>
	.rail {
		display: flex;
		flex-direction: column;
		width: var(--rail-w);
		background: var(--canvas);
		color: var(--ink);
		overflow: hidden;
		min-height: 0;
		border-right: 1px solid var(--border);
	}

	.rail-close {
		display: none;
		align-items: center;
		justify-content: center;
		width: var(--tap);
		height: var(--tap);
		border-radius: var(--radius-sm);
		color: var(--ink-3);
	}
	.rail-close:hover {
		color: var(--ink);
		background: var(--surface-2);
	}

	@media (max-width: 720px) {
		.rail {
			position: fixed;
			top: var(--topbar-h);
			bottom: var(--statusbar-h);
			left: 0;
			width: min(86vw, 340px);
			z-index: 50;
			transform: translateX(-100%);
			transition: transform 180ms ease-out;
			box-shadow: var(--shadow-pop);
		}
		.rail.is-open {
			transform: translateX(0);
		}
		.rail-close {
			display: inline-flex;
		}
	}
	@media (prefers-reduced-motion: reduce) {
		.rail {
			transition: none;
		}
	}

	.rail-head {
		display: flex;
		justify-content: space-between;
		align-items: center;
		gap: var(--s-2);
		padding: var(--s-3) var(--s-4);
		min-height: var(--tap);
		border-bottom: 1px solid var(--border);
	}
	.counts {
		font-size: var(--fs-xs);
		color: var(--ink-3);
		display: inline-flex;
		gap: 5px;
		align-items: baseline;
		margin-left: auto;
		font-family: var(--ff-mono);
	}
	.counts .num {
		color: var(--ink);
		font-weight: 700;
	}

	.groups {
		flex: 1;
		overflow-y: auto;
		padding-block: var(--s-2);
	}
	.group + .group {
		margin-top: var(--s-4);
	}
	.group-head {
		display: flex;
		justify-content: space-between;
		align-items: baseline;
		padding: var(--s-1) var(--s-4) var(--s-2);
	}
	.group-count {
		font-size: var(--fs-xs);
		color: var(--ink-3);
	}

	ul {
		list-style: none;
		margin: 0;
		padding: 0 var(--s-2);
	}
	.row {
		width: 100%;
		display: grid;
		grid-template-columns: auto 1fr auto;
		grid-template-areas:
			'dot name meta'
			'dot peer meta';
		column-gap: var(--s-3);
		align-items: center;
		padding: var(--s-2) var(--s-3);
		min-height: var(--tap);
		border-radius: var(--radius);
		font-family: var(--ff-body);
		font-size: var(--fs-md);
		color: var(--ink);
		text-align: left;
	}
	.row :global(.dot) {
		grid-area: dot;
		align-self: center;
	}
	.name {
		grid-area: name;
		font-weight: 700;
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}
	.peer {
		grid-area: peer;
		font-size: var(--fs-xs);
		color: var(--ink-3);
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}
	.last,
	.unread {
		grid-area: meta;
		font-size: var(--fs-xs);
		text-align: right;
		justify-self: end;
	}
	.last {
		color: var(--ink-3);
	}
	.unread {
		color: var(--on-accent);
		background: var(--accent);
		padding: 1px 6px;
		border-radius: 999px;
		font-weight: 700;
		min-width: 18px;
		text-align: center;
	}

	.row:hover {
		background: var(--surface-2);
	}
	.row.active {
		background: var(--accent-tint);
	}
	.row.active .name {
		color: var(--accent-strong);
	}

	.rail-foot {
		border-top: 1px solid var(--border);
		padding: var(--s-2) var(--s-3);
	}
	.add {
		display: inline-flex;
		align-items: center;
		gap: var(--s-2);
		color: var(--ink-2);
		padding: 0 var(--s-2);
		min-height: var(--tap);
		font-size: var(--fs-sm);
		font-weight: 700;
		border-radius: var(--radius-sm);
	}
	.plus {
		font-family: var(--ff-mono);
		font-weight: 700;
		font-size: var(--fs-md);
	}
</style>
