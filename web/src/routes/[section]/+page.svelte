<script lang="ts">
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { sectionMap } from '$lib/data/sections';

	const param = $derived(page.params.section ?? '');
	const section = $derived(sectionMap[param]);

	// /chat is a friendly alias for the chat workspace at /. The
	// previous dashboard default routed there, and muscle memory
	// from other chat apps lands users there too. Bounce client-
	// side so the URL self-corrects without a "not found" flash.
	$effect(() => {
		// Preserve any auth-token fragment so the redirect doesn't
		// log the user out. goto() honors the supplied URL verbatim;
		// `location.hash` already includes the leading '#'.
		if (param === 'chat') goto('/' + location.search + location.hash, { replaceState: true });
	});
</script>

<section class="panel" aria-labelledby="section-title">
	{#if param === 'chat'}
		<!-- Redirecting to /. Effect above handles the navigation; this
		     branch keeps the page from rendering a not-found flash. -->
		<p class="sub">Opening chat…</p>
	{:else if section}
		<header class="head">
			<h1 id="section-title" class="title">{section.label}</h1>
			<span class="unwired t-mono">unwired</span>
		</header>
		<p class="sub">{section.desc}</p>

		<div class="placeholder" role="presentation">
			<p class="ph-text">
				Placeholder for the <strong>{section.label.toLowerCase()}</strong> control surface.
				The RPC behind it is not wired yet.
			</p>
		</div>
	{:else}
		<header class="head">
			<h1 id="section-title" class="title">Not found</h1>
		</header>
		<p class="sub">No section named “{param}”.</p>
	{/if}
</section>

<style>
	.panel {
		flex: 1;
		overflow-y: auto;
		padding: var(--s-8) var(--s-10);
		background: var(--surface);
		color: var(--ink);
	}
	.head {
		display: flex;
		align-items: baseline;
		gap: var(--s-3);
	}
	.title {
		font-size: var(--fs-lg);
		font-weight: 700;
	}
	.unwired {
		font-size: var(--fs-xs);
		color: var(--ink-3);
		padding: 1px 6px;
		border: 1px solid var(--border);
		border-radius: 999px;
	}
	.sub {
		margin: var(--s-2) 0 var(--s-6);
		color: var(--ink-2);
		font-size: var(--fs-md);
		max-width: 60ch;
	}
	.placeholder {
		border: 1px dashed var(--border-strong);
		border-radius: var(--radius);
		padding: var(--s-8);
		background: var(--canvas);
	}
	.ph-text {
		margin: 0;
		color: var(--ink-3);
		font-size: var(--fs-sm);
		max-width: 60ch;
	}
	.ph-text strong {
		color: var(--ink-2);
		font-weight: 700;
	}

	@media (max-width: 720px) {
		.panel {
			padding: var(--s-5) var(--s-4);
		}
	}
</style>
