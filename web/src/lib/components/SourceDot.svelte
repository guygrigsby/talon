<script lang="ts">
	import type { Source, ChannelStatus } from '$lib/data/channels';

	let {
		source,
		status = 'connected',
		size = 7,
	}: { source: Source; status?: ChannelStatus; size?: number } = $props();
</script>

<span
	class="dot s-{status}"
	style="--src: var(--src-{source}); --d: {size}px"
	role="img"
	aria-label="{source} {status}"
></span>

<style>
	.dot {
		display: inline-block;
		width: var(--d);
		height: var(--d);
		border-radius: 50%;
		background: var(--src);
		flex-shrink: 0;
		vertical-align: middle;
	}
	.s-connecting {
		animation: breathe 2.2s ease-in-out infinite;
	}
	.s-disconnected {
		background: transparent;
		box-shadow: inset 0 0 0 1.5px var(--ink-3);
	}
	.s-error {
		background: var(--err);
	}
	@keyframes breathe {
		0%, 100% { opacity: 0.35; }
		50% { opacity: 1; }
	}
	@media (prefers-reduced-motion: reduce) {
		.s-connecting {
			animation: none;
			background: transparent;
			box-shadow: inset 0 0 0 1.5px var(--src);
		}
	}
</style>
