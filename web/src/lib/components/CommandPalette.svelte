<script lang="ts">
	import { goto } from '$app/navigation';
	import { sections } from '$lib/data/sections';
	import { chrome } from '$lib/state/chrome.svelte';

	let { open = false, onClose }: { open?: boolean; onClose?: () => void } = $props();

	type Entry = {
		id: string;
		group: string;
		label: string;
		hint?: string;
		kind?: string;
		run?: () => void;
	};

	function close() {
		onClose?.();
	}

	const ALL: Entry[] = [
		...sections.map((s) => ({
			id: `admin:${s.key}`,
			group: 'Admin',
			label: s.key,
			hint: s.desc,
			kind: 'admin',
			run: () => goto(`/${s.key}`),
		})),
		{ id: 'go:chat', group: 'Actions', label: 'Go to chat', kind: 'nav', run: () => goto('/') },
		{
			id: 'tg:inspector',
			group: 'Actions',
			label: 'Toggle inspector',
			kind: 'view',
			run: () => chrome.toggleInspector(),
		},
	];

	const GROUP_ORDER = ['Admin', 'Actions'];

	let query = $state('');
	let activeIndex = $state(0);
	let inputEl: HTMLInputElement | undefined = $state();
	let trigger: HTMLElement | null = null;
	let wasOpen = false;

	function subseq(haystack: string, needle: string): boolean {
		if (!needle) return true;
		let i = 0;
		for (const ch of haystack) {
			if (ch === needle[i]) i++;
			if (i === needle.length) return true;
		}
		return false;
	}

	const view = $derived.by(() => {
		const q = query.trim().toLowerCase();
		let idx = 0;
		const groups: Array<{ name: string; items: Array<{ entry: Entry; index: number }> }> = [];
		const flat: Entry[] = [];
		for (const g of GROUP_ORDER) {
			const items = ALL.filter(
				(e) => e.group === g && subseq(`${e.label} ${e.hint ?? ''}`.toLowerCase(), q),
			).map((entry) => {
				flat.push(entry);
				return { entry, index: idx++ };
			});
			if (items.length) groups.push({ name: g, items });
		}
		return { groups, flat, count: idx };
	});

	// reset highlight whenever the query changes (and on first mount)
	$effect(() => {
		query;
		activeIndex = 0;
	});

	// open / close lifecycle: capture trigger, focus input, restore on close
	$effect(() => {
		if (open && !wasOpen) {
			trigger = (document.activeElement as HTMLElement) ?? null;
			query = '';
			activeIndex = 0;
			queueMicrotask(() => inputEl?.focus());
		} else if (!open && wasOpen) {
			trigger?.focus?.();
		}
		wasOpen = open;
	});

	// keep the highlighted option in view
	$effect(() => {
		if (!open) return;
		activeIndex;
		queueMicrotask(() => {
			document.getElementById(`cmdk-opt-${activeIndex}`)?.scrollIntoView({ block: 'nearest' });
		});
	});

	function choose(entry: Entry | undefined) {
		if (!entry) return;
		close();
		entry.run?.();
	}

	function onKeydown(e: KeyboardEvent) {
		switch (e.key) {
			case 'ArrowDown':
				e.preventDefault();
				activeIndex = view.count ? Math.min(activeIndex + 1, view.count - 1) : 0;
				break;
			case 'ArrowUp':
				e.preventDefault();
				activeIndex = Math.max(activeIndex - 1, 0);
				break;
			case 'Home':
				e.preventDefault();
				activeIndex = 0;
				break;
			case 'End':
				e.preventDefault();
				activeIndex = Math.max(view.count - 1, 0);
				break;
			case 'Enter':
				e.preventDefault();
				choose(view.flat[activeIndex]);
				break;
			case 'Escape':
				e.preventDefault();
				close();
				break;
			case 'Tab':
				// focus trap — only the input is focusable inside the dialog
				e.preventDefault();
				break;
		}
	}
</script>

{#if open}
	<div class="overlay">
		<button type="button" class="scrim" aria-label="Close command palette" onclick={close}></button>

		<div class="panel" role="dialog" aria-modal="true" aria-label="Command palette">
			<div class="search">
				<svg class="search-icon" viewBox="0 0 24 24" width="18" height="18" aria-hidden="true">
					<circle cx="11" cy="11" r="6.5" stroke="currentColor" stroke-width="2" fill="none" />
					<path d="M16 16 L 21 21" stroke="currentColor" stroke-width="2" fill="none" />
				</svg>
				<input
					bind:this={inputEl}
					bind:value={query}
					onkeydown={onKeydown}
					class="search-input"
					type="text"
					role="combobox"
					aria-expanded="true"
					aria-controls="cmdk-list"
					aria-activedescendant={view.count ? `cmdk-opt-${activeIndex}` : undefined}
					aria-autocomplete="list"
					aria-label="Jump to an admin section or action"
					placeholder="Jump to…"
					autocomplete="off"
					spellcheck="false"
				/>
				<kbd class="esc t-mono">esc</kbd>
			</div>

			<ul id="cmdk-list" class="list" role="listbox" aria-label="Results">
				{#each view.groups as group (group.name)}
					<li class="group" role="presentation">
						<span class="group-name t-label">{group.name}</span>
						<ul role="presentation">
							{#each group.items as { entry, index } (entry.id)}
								<!-- Keyboard nav handled on the combobox input via
								     aria-activedescendant (WAI-ARIA APG); options aren't focusable. -->
								<!-- svelte-ignore a11y_click_events_have_key_events -->
								<li
									id="cmdk-opt-{index}"
									class="opt"
									class:active={index === activeIndex}
									role="option"
									aria-selected={index === activeIndex}
									onmousemove={() => (activeIndex = index)}
									onclick={() => choose(entry)}
								>
									<span class="opt-icon" aria-hidden="true">
										<span class="glyph">›</span>
									</span>
									<span class="opt-label">{entry.label}</span>
									{#if entry.hint}
										<span class="opt-hint">{entry.hint}</span>
									{/if}
									{#if entry.kind}
										<span class="opt-kind t-mono">{entry.kind}</span>
									{/if}
								</li>
							{/each}
						</ul>
					</li>
				{:else}
					<li class="empty" role="presentation">No matches for "{query}"</li>
				{/each}
			</ul>

			<footer class="legend" aria-hidden="true">
				<span><kbd class="t-mono">↑</kbd><kbd class="t-mono">↓</kbd> navigate</span>
				<span><kbd class="t-mono">↵</kbd> select</span>
				<span><kbd class="t-mono">esc</kbd> close</span>
			</footer>
		</div>
	</div>
{/if}

<style>
	.overlay {
		position: fixed;
		inset: 0;
		z-index: 200;
		display: flex;
		justify-content: center;
		align-items: flex-start;
		padding: 10vh var(--s-4) var(--s-4);
	}
	.scrim {
		position: absolute;
		inset: 0;
		background: rgba(24, 24, 26, 0.32);
		backdrop-filter: blur(2px);
		border: 0;
		padding: 0;
		cursor: default;
		animation: scrim-in 140ms ease-out;
	}

	.panel {
		position: relative;
		width: min(560px, 100%);
		max-height: 70vh;
		display: flex;
		flex-direction: column;
		background: var(--surface);
		border: 1px solid var(--border-strong);
		border-radius: var(--radius);
		box-shadow: var(--shadow-pop);
		overflow: hidden;
		animation: panel-in 160ms cubic-bezier(0.16, 1, 0.3, 1);
	}

	.search {
		display: flex;
		align-items: center;
		gap: var(--s-3);
		padding: 0 var(--s-3);
		border-bottom: 1px solid var(--border);
		min-height: 52px;
	}
	.search-icon {
		color: var(--ink-3);
		flex-shrink: 0;
	}
	.search-input {
		flex: 1;
		border: 0;
		outline: 0;
		background: transparent;
		font-family: var(--ff-body);
		font-size: var(--fs-lg);
		color: var(--ink);
		min-height: var(--tap);
	}
	.search-input::placeholder {
		color: var(--ink-3);
	}
	.search-input:focus-visible {
		box-shadow: none;
	}
	.esc {
		font-size: var(--fs-xs);
		color: var(--ink-3);
		background: var(--surface);
		border: 1px solid var(--border);
		border-radius: var(--radius-sm);
		padding: 2px 6px;
		flex-shrink: 0;
	}

	.list {
		list-style: none;
		margin: 0;
		padding: var(--s-2);
		overflow-y: auto;
		flex: 1;
	}
	.list ul {
		list-style: none;
		margin: 0;
		padding: 0;
	}
	.group + .group {
		margin-top: var(--s-2);
	}
	.group-name {
		display: block;
		padding: var(--s-2) var(--s-2) var(--s-1);
		color: var(--ink-3);
	}

	.opt {
		display: grid;
		grid-template-columns: auto 1fr auto;
		align-items: center;
		gap: var(--s-3);
		padding: var(--s-2) var(--s-2);
		min-height: var(--tap);
		border-radius: var(--radius-sm);
		cursor: pointer;
		color: var(--ink);
	}
	.opt-icon {
		display: inline-flex;
		align-items: center;
		justify-content: center;
		width: 18px;
	}
	.glyph {
		font-family: var(--ff-mono);
		font-weight: 700;
		color: var(--ink-3);
	}
	.opt-label {
		font-weight: 700;
		font-size: var(--fs-md);
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}
	.opt-hint {
		font-size: var(--fs-sm);
		color: var(--ink-3);
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
		justify-self: start;
	}
	.opt-kind {
		font-size: var(--fs-xs);
		color: var(--ink-3);
		text-transform: uppercase;
		letter-spacing: var(--tracking-caps);
		justify-self: end;
	}

	.opt.active {
		background: var(--accent-tint);
		box-shadow: inset 2px 0 0 var(--accent);
	}
	.opt.active .opt-label {
		color: var(--accent-strong);
	}
	.opt.active .glyph {
		color: var(--accent);
	}

	.empty {
		padding: var(--s-5) var(--s-3);
		text-align: center;
		color: var(--ink-3);
		font-size: var(--fs-sm);
	}

	.legend {
		display: flex;
		gap: var(--s-4);
		padding: var(--s-2) var(--s-3);
		border-top: 1px solid var(--border);
		background: var(--canvas);
		font-size: var(--fs-xs);
		color: var(--ink-3);
	}
	.legend span {
		display: inline-flex;
		align-items: center;
		gap: 4px;
	}
	.legend kbd {
		font-size: var(--fs-xs);
		color: var(--ink-2);
		background: var(--surface);
		border: 1px solid var(--border);
		border-radius: var(--radius-sm);
		padding: 0 5px;
		min-width: 18px;
		text-align: center;
	}

	@keyframes scrim-in {
		from { opacity: 0; }
		to { opacity: 1; }
	}
	@keyframes panel-in {
		from { opacity: 0; transform: translateY(-8px) scale(0.98); }
		to { opacity: 1; transform: translateY(0) scale(1); }
	}

	@media (max-width: 720px) {
		.overlay {
			padding: 8vh var(--s-3) var(--s-3);
		}
		.opt-hint {
			display: none;
		}
	}
</style>
