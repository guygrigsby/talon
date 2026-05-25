<script lang="ts">
	import { loadSelectableModels, modelKey, type ModelEntry } from '$lib/gateway/models';

	let {
		value = '',
		onChange,
		disabled = false,
		defaultLabel = null
	}: {
		value?: string;
		onChange?: (modelId: string) => void;
		disabled?: boolean;
		// When the picker offers an "agent default" option, render
		// what that default actually resolves to (e.g.
		// "deepseek/deepseek-chat") instead of the bare phrase.
		defaultLabel?: string | null;
	} = $props();

	let models = $state<ModelEntry[]>([]);
	let loadError = $state<string | null>(null);

	$effect(() => {
		loadSelectableModels()
			.then((m) => (models = m))
			.catch((err) => (loadError = err instanceof Error ? err.message : String(err)));
	});

	// Grouped by provider for the optgroup labels. Authed providers
	// come first because loadSelectableModels already sorts that way.
	const groups = $derived.by(() => {
		const map = new Map<string, ModelEntry[]>();
		for (const m of models) {
			if (!map.has(m.provider)) map.set(m.provider, []);
			map.get(m.provider)!.push(m);
		}
		return [...map.entries()];
	});

	function onSelect(e: Event) {
		const target = e.currentTarget as HTMLSelectElement;
		onChange?.(target.value);
	}
</script>

<label class="model-picker">
	<span class="t-label">model</span>
	<select
		class="t-mono"
		{value}
		{disabled}
		onchange={onSelect}
		aria-label="Model for this session"
	>
		<option value="">{defaultLabel ? `agent default · ${defaultLabel}` : 'agent default'}</option>
		{#each groups as [provider, entries] (provider)}
			<optgroup label={provider}>
				{#each entries as m (m.id)}
					<option value={modelKey(m)} disabled={!m.authOk}>
						{m.name}{m.authOk ? '' : ' · no auth'}
					</option>
				{/each}
			</optgroup>
		{/each}
	</select>
	{#if loadError}
		<span class="err t-mono" title={loadError}>load failed</span>
	{/if}
</label>

<style>
	.model-picker {
		display: inline-flex;
		align-items: center;
		gap: var(--s-2);
	}
	select {
		appearance: none;
		background: var(--canvas);
		color: var(--ink);
		border: 1px solid var(--border);
		border-radius: var(--radius);
		padding: 4px var(--s-2);
		font-size: var(--fs-xs);
		min-height: var(--tap, 32px);
		cursor: pointer;
	}
	select:disabled {
		opacity: 0.6;
		cursor: not-allowed;
	}
	select:focus-visible {
		outline: 2px solid var(--accent);
		outline-offset: 2px;
	}
	.err {
		color: var(--accent-strong, var(--ink));
		font-size: var(--fs-xs);
	}
</style>
