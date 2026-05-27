// Enumerate configured gateway channels by reading `channels.*` out of the
// merged config. Each configured channel becomes a compact source-status entry
// in chat. No typed ChannelsService.List endpoint yet; when one lands we'd swap
// this out for the typed call and drop the config-shape coupling.

import { create } from '@bufbuild/protobuf';
import { ConfigGetRequestSchema } from './gen/talon/v1/config_pb.js';
import { getConfigClient } from './connect.js';
import type { Channel, Source } from '../data/channels';

// Which channel name keys in config map onto which Source label.
// New first-party channels get added here when their plugin lands.
const sourceForChannel: Record<string, Source> = {
	telegram: 'telegram',
	bluebubbles: 'imessage',
	whisper: 'whisper'
};

type ChannelsBlock = Record<string, Record<string, unknown> | undefined>;
type ConfigEnvelope = { config?: { channels?: ChannelsBlock } };

export async function loadConfiguredChannels(): Promise<Channel[]> {
	// ConfigService.Get ignores the path param today and always
	// returns the full merged config; we drill into config.channels
	// ourselves. When config.get gains real path support we can
	// pass "channels" again and use the response directly.
	const res = await getConfigClient().get(create(ConfigGetRequestSchema, { path: '' }));
	if (!res.json) return [];
	const envelope = JSON.parse(res.json) as ConfigEnvelope;
	const cfg = envelope?.config?.channels ?? {};
	const out: Channel[] = [];
	for (const [name, sub] of Object.entries(cfg)) {
		if (!sub || typeof sub !== 'object') continue;
		const source = sourceForChannel[name] ?? 'cli';
		// Heuristic for "configured": botToken / equivalent secret
		// is non-empty. Telegram is the only concrete case today.
		const hasToken = typeof sub.botToken === 'string' && sub.botToken.length > 0;
		const enabled = sub.enabled !== false;
		out.push({
			id: `ch-${name}`,
			source,
			name,
			peer: hasToken ? 'token set' : 'unconfigured',
			unread: 0,
			lastActive: 'now',
			status: hasToken && enabled ? 'connected' : 'disconnected'
		});
	}
	out.sort((a, b) => a.name.localeCompare(b.name));
	return out;
}
