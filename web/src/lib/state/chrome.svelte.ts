// Shared chrome state — inspector visibility, viewport class, and palette.
// Pages and the layout both read/write this; runes in a .svelte.ts module
// give us reactive globals without a store ceremony.

class Chrome {
	inspectorOpen = $state(true);
	isNarrow = $state(false);
	paletteOpen = $state(false);

	private cleanup: (() => void) | null = null;

	startWatching() {
		if (typeof window === 'undefined' || this.cleanup) return;
		const mql = window.matchMedia('(max-width: 720px)');
		const apply = () => {
			this.isNarrow = mql.matches;
			this.inspectorOpen = !mql.matches;
		};
		apply();
		mql.addEventListener('change', apply);
		this.cleanup = () => mql.removeEventListener('change', apply);
	}

	stopWatching() {
		this.cleanup?.();
		this.cleanup = null;
	}

	toggleInspector() {
		this.inspectorOpen = !this.inspectorOpen;
	}
	closePanelsOnNarrow() {
		if (this.isNarrow) {
			this.inspectorOpen = false;
		}
	}

	openPalette() {
		this.paletteOpen = true;
	}
	closePalette() {
		this.paletteOpen = false;
	}
	togglePalette() {
		this.paletteOpen = !this.paletteOpen;
	}
}

export const chrome = new Chrome();
