import { a as buildProviderReplayFamilyHooks } from "../../provider-model-shared-DYKze5ii.js";
import { t as defineSingleProviderPluginEntry } from "../../provider-entry-CDOgD70E.js";
import { i as MOONSHOT_THINKING_STREAM_HOOKS } from "../../provider-stream-U9gr3YL8.js";
import "../../provider-stream-family-B__-4zUv.js";
import { a as buildMoonshotProvider, i as applyMoonshotNativeStreamingUsageCompat } from "../../provider-catalog-itH4SOby.js";
import { n as applyMoonshotConfig, r as applyMoonshotConfigCn, t as MOONSHOT_DEFAULT_MODEL_REF } from "../../onboard-B0ujBKgr.js";
import "../../api-CPJvVCYj.js";
import { r as moonshotMediaUnderstandingProvider } from "../../media-understanding-provider-Raasxxye.js";
import { t as createKimiWebSearchProvider } from "../../kimi-web-search-provider-vIzdnZjR.js";
var moonshot_default = defineSingleProviderPluginEntry({
	id: "moonshot",
	name: "Moonshot Provider",
	description: "Bundled Moonshot provider plugin",
	provider: {
		label: "Moonshot",
		docsPath: "/providers/moonshot",
		auth: [{
			methodId: "api-key",
			label: "Kimi API key (.ai)",
			hint: "Kimi K2.6 + Kimi",
			optionKey: "moonshotApiKey",
			flagName: "--moonshot-api-key",
			envVar: "MOONSHOT_API_KEY",
			promptMessage: "Enter Moonshot API key",
			defaultModel: MOONSHOT_DEFAULT_MODEL_REF,
			applyConfig: (cfg) => applyMoonshotConfig(cfg),
			wizard: { groupLabel: "Moonshot AI (Kimi K2.6)" }
		}, {
			methodId: "api-key-cn",
			label: "Kimi API key (.cn)",
			hint: "Kimi K2.6 + Kimi",
			optionKey: "moonshotApiKey",
			flagName: "--moonshot-api-key",
			envVar: "MOONSHOT_API_KEY",
			promptMessage: "Enter Moonshot API key (.cn)",
			defaultModel: MOONSHOT_DEFAULT_MODEL_REF,
			applyConfig: (cfg) => applyMoonshotConfigCn(cfg),
			wizard: { groupLabel: "Moonshot AI (Kimi K2.6)" }
		}],
		catalog: {
			buildProvider: buildMoonshotProvider,
			buildStaticProvider: buildMoonshotProvider,
			allowExplicitBaseUrl: true
		},
		applyNativeStreamingUsageCompat: ({ providerConfig }) => applyMoonshotNativeStreamingUsageCompat(providerConfig),
		...buildProviderReplayFamilyHooks({
			family: "openai-compatible",
			sanitizeToolCallIds: false
		}),
		...MOONSHOT_THINKING_STREAM_HOOKS,
		resolveThinkingProfile: () => ({
			levels: [{
				id: "off",
				label: "off"
			}, {
				id: "low",
				label: "on"
			}],
			defaultLevel: "off"
		})
	},
	register(api) {
		api.registerMediaUnderstandingProvider(moonshotMediaUnderstandingProvider);
		api.registerWebSearchProvider(createKimiWebSearchProvider());
	}
});
//#endregion
export { moonshot_default as default };
