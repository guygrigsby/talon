// Pure SPA: no SSR, no prerender. The Go binary serves the static fallback
// (index.html) for every non-asset path and the client takes over from there.
export const prerender = false;
export const ssr = false;
