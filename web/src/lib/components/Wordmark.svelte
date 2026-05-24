<script lang="ts">
	// talon wordmark — the glyph reads as a hooked talon, inked in the one
	// accent. Word is plain bold lowercase; no display face anywhere.
	let { compact = false }: { compact?: boolean } = $props();
</script>

<a href="/" class="lockup" class:compact aria-label="talon — home">
	<svg viewBox="0 0 24 24" width="20" height="20" aria-hidden="true" focusable="false">
		<path
			d="M3 21 C 8 18, 12 14, 14 9 C 15 6, 17 4, 21 3 L 20 6 C 17 8, 16 11, 15 14 C 13 18, 9 21, 3 21 Z"
			fill="var(--accent)"
		/>
	</svg>
	{#if !compact}
		<span class="word">talon</span>
	{/if}
</a>

<style>
	.lockup {
		display: inline-flex;
		align-items: center;
		gap: var(--s-2);
		color: var(--ink);
		min-height: var(--hit-min);
	}
	.word {
		font-family: var(--ff-body);
		font-weight: 700;
		font-size: var(--fs-md);
		line-height: 1;
		letter-spacing: -0.01em;
		color: var(--ink);
	}
</style>
