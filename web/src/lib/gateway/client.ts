// Stub for the talon gateway WebSocket client. The real implementation
// (framing, reconnect, auth via URL-fragment token a la `talon dashboard`)
// lands in a follow-up.

export type GatewayConfig = {
	url: string;
	token?: string;
};

export function defaultConfig(): GatewayConfig {
	const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
	const url = `${proto}//${location.host}/ws`;
	const token = new URLSearchParams(location.hash.slice(1)).get('token') ?? undefined;
	return { url, token };
}
