<script lang="ts">
	import type { AgentEntry } from '$lib/gateway/agents';

	let {
		agents = [],
		value = '',
		onChange,
		disabled = false
	}: {
		agents?: AgentEntry[];
		value?: string;
		onChange?: (agentId: string) => void;
		disabled?: boolean;
	} = $props();

	function onSelect(e: Event) {
		const target = e.currentTarget as HTMLSelectElement;
		onChange?.(target.value);
	}
</script>

<label class="agent-picker">
	<span class="t-label">agent</span>
	<select
		class="t-mono"
		{value}
		{disabled}
		onchange={onSelect}
		aria-label="Agent for this session"
	>
		{#each agents as a (a.id)}
			<option value={a.id}>{a.name} · {a.primaryModelName || a.primaryModel}</option>
		{/each}
	</select>
</label>

<style>
	.agent-picker {
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
</style>
