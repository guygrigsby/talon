// Typed Connect clients for every talon.v1.* service. Each `getX()`
// helper memoizes one client per service so callers don't recreate
// the transport on every render.
//
// Transport: HTTP/JSON over the same origin as the SvelteKit app.
// In production the gateway serves the SPA itself, so same-origin
// is automatic. In dev Vite proxies talon.v1.* to the gateway (see
// vite.config.ts), so same-origin still applies.
//
// Auth: Bearer token from the URL fragment (matches the WS client's
// `talon dashboard` handoff convention). Pulled once per transport
// creation; if the token rotates mid-session, callers can force a
// reset by reloading the page — this matches what the WS client
// does today.

import { createConnectTransport } from '@connectrpc/connect-web';
import { createClient, type Client } from '@connectrpc/connect';

import { InfraService } from './gen/talon/v1/infra_pb.js';
import { ConfigService } from './gen/talon/v1/config_pb.js';
import { AgentsService } from './gen/talon/v1/agents_pb.js';
import { ModelsService } from './gen/talon/v1/models_pb.js';
import { SessionsService } from './gen/talon/v1/sessions_pb.js';
import { ChatService } from './gen/talon/v1/chat_pb.js';
import { PluginsService } from './gen/talon/v1/plugins_pb.js';
import { CronService } from './gen/talon/v1/cron_pb.js';
import { ChannelsService } from './gen/talon/v1/channels_pb.js';

function bearerToken(): string | undefined {
	if (typeof location === 'undefined') return undefined;
	const params = new URLSearchParams(location.hash.slice(1));
	return params.get('token') ?? undefined;
}

let cachedTransport: ReturnType<typeof createConnectTransport> | undefined;

function transport() {
	if (cachedTransport) return cachedTransport;
	const token = bearerToken();
	cachedTransport = createConnectTransport({
		baseUrl: typeof location === 'undefined' ? '/' : location.origin,
		// Header injection runs per-call so a token added after
		// transport creation also takes effect, though today the
		// token is captured once at startup (see file header).
		interceptors: token
			? [
					(next) => async (req) => {
						req.header.set('Authorization', `Bearer ${token}`);
						return next(req);
					}
				]
			: undefined
	});
	return cachedTransport;
}

let infraClient: Client<typeof InfraService> | undefined;
let configClient: Client<typeof ConfigService> | undefined;
let agentsClient: Client<typeof AgentsService> | undefined;
let modelsClient: Client<typeof ModelsService> | undefined;
let sessionsClient: Client<typeof SessionsService> | undefined;
let chatClient: Client<typeof ChatService> | undefined;
let pluginsClient: Client<typeof PluginsService> | undefined;
let cronClient: Client<typeof CronService> | undefined;
let channelsClient: Client<typeof ChannelsService> | undefined;

export function getInfraClient() {
	return (infraClient ??= createClient(InfraService, transport()));
}
export function getConfigClient() {
	return (configClient ??= createClient(ConfigService, transport()));
}
export function getAgentsClient() {
	return (agentsClient ??= createClient(AgentsService, transport()));
}
export function getModelsClient() {
	return (modelsClient ??= createClient(ModelsService, transport()));
}
export function getSessionsClient() {
	return (sessionsClient ??= createClient(SessionsService, transport()));
}
export function getChatClient() {
	return (chatClient ??= createClient(ChatService, transport()));
}
export function getPluginsClient() {
	return (pluginsClient ??= createClient(PluginsService, transport()));
}
export function getCronClient() {
	return (cronClient ??= createClient(CronService, transport()));
}
export function getChannelsClient() {
	return (channelsClient ??= createClient(ChannelsService, transport()));
}
