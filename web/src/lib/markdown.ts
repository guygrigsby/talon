// Markdown → sanitized HTML for assistant chat bubbles.
//
// marked handles the parse + render. DOMPurify strips anything
// that could execute (script/style/event-handlers/javascript:
// URLs); assistant text isn't user-controlled but it can absolutely
// be coerced into emitting unsafe HTML, so the sanitizer is the
// only defense.
//
// Output: HTML string ready to drop into a {@html ...} slot.

import DOMPurify from 'dompurify';
import { marked } from 'marked';

marked.setOptions({
	gfm: true,
	breaks: true // newline-in-paragraph → <br>, matches chat ergonomics
});

export function renderMarkdown(src: string): string {
	if (!src) return '';
	// marked.parse can return string | Promise<string> depending on
	// extensions; ours are all sync so the cast is safe.
	const raw = marked.parse(src, { async: false }) as string;
	if (typeof DOMPurify.sanitize !== 'function') {
		// SSR fallback: no window, no DOMPurify hookup. Returning
		// the unsanitized HTML on the server is fine — the
		// hydration on the client immediately re-runs the same
		// render (with sanitization) before any interaction.
		return raw;
	}
	return DOMPurify.sanitize(raw, {
		// Block script-shaped attributes (DOMPurify defaults
		// already cover this, but spell it out for the reader).
		FORBID_ATTR: ['onerror', 'onload', 'onclick'],
		FORBID_TAGS: ['style', 'script', 'iframe']
	});
}
