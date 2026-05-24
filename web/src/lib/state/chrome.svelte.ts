// Shared chrome state — rail/inspector visibility + viewport class.
// Pages and the layout both read/write this; runes in a .svelte.ts module
// give us reactive globals without a store ceremony.

class Chrome {
	railOpen = $state(true);
	inspectorOpen = $state(true);
	isNarrow = $state(false);
	paletteOpen = $state(false);

	private cleanup: (() => void) | null = null;

	startWatching() {
		if (typeof window === 'undefined' || this.cleanup) return;
		const mql = window.matchMedia('(max-width: 720px)');
		const apply = () => {
			this.isNarrow = mql.matches;
			this.railOpen = !mql.matches;
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

	toggleRail() {
		this.railOpen = !this.railOpen;
	}
	toggleInspector() {
		this.inspectorOpen = !this.inspectorOpen;
	}
	closeAllOnNarrow() {
		if (this.isNarrow) {
			this.railOpen = false;
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
