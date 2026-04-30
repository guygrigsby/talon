import { a as buildProviderToolCompatFamilyHooks } from "../../provider-tools-CbCRJNi3.js";
import { t as definePluginEntry } from "../../plugin-entry-BpVWBiQw.js";
import { r as resolvePluginConfigObject } from "../../config-runtime-CLgBc5VJ.js";
import { t as buildOpenAICodexCliBackend } from "../../cli-backend-VrIKccFZ.js";
import { t as buildOpenAIImageGenerationProvider } from "../../image-generation-provider-BfoQmZsI.js";
import { n as openaiCodexMediaUnderstandingProvider, r as openaiMediaUnderstandingProvider } from "../../media-understanding-provider-CZgwfiPH.js";
import { t as openAiMemoryEmbeddingProviderAdapter } from "../../memory-embedding-adapter-zWfo8orX.js";
import { t as buildOpenAICodexProviderPlugin } from "../../openai-codex-provider-DX0Hgrzv.js";
import { t as buildOpenAIProvider } from "../../openai-provider-CvWGFhsX.js";
import { i as resolveOpenAISystemPromptContribution, r as resolveOpenAIPromptOverlayMode } from "../../prompt-overlay-bjx0UFiM.js";
import { t as buildOpenAIRealtimeTranscriptionProvider } from "../../realtime-transcription-provider-CadywjfC.js";
import { t as buildOpenAIRealtimeVoiceProvider } from "../../realtime-voice-provider-CZ_M6Je9.js";
import { t as buildOpenAISpeechProvider } from "../../speech-provider-C4PHqsCM.js";
import { t as buildOpenAIVideoGenerationProvider } from "../../video-generation-provider-Am1BPGmM.js";
//#region extensions/openai/index.ts
var openai_default = definePluginEntry({
	id: "openai",
	name: "OpenAI Provider",
	description: "Bundled OpenAI provider plugins",
	register(api) {
		const openAIToolCompatHooks = buildProviderToolCompatFamilyHooks("openai");
		const buildProviderWithPromptContribution = (provider) => ({
			...provider,
			...openAIToolCompatHooks,
			resolveSystemPromptContribution: (ctx) => {
				const pluginConfig = resolvePluginConfigObject(ctx.config, "openai") ?? (ctx.config ? void 0 : api.pluginConfig);
				return resolveOpenAISystemPromptContribution({
					config: ctx.config,
					legacyPluginConfig: pluginConfig,
					mode: resolveOpenAIPromptOverlayMode(pluginConfig),
					modelProviderId: provider.id,
					modelId: ctx.modelId
				});
			}
		});
		api.registerCliBackend(buildOpenAICodexCliBackend());
		api.registerProvider(buildProviderWithPromptContribution(buildOpenAIProvider()));
		api.registerProvider(buildProviderWithPromptContribution(buildOpenAICodexProviderPlugin()));
		api.registerMemoryEmbeddingProvider(openAiMemoryEmbeddingProviderAdapter);
		api.registerImageGenerationProvider(buildOpenAIImageGenerationProvider());
		api.registerRealtimeTranscriptionProvider(buildOpenAIRealtimeTranscriptionProvider());
		api.registerRealtimeVoiceProvider(buildOpenAIRealtimeVoiceProvider());
		api.registerSpeechProvider(buildOpenAISpeechProvider());
		api.registerMediaUnderstandingProvider(openaiMediaUnderstandingProvider);
		api.registerMediaUnderstandingProvider(openaiCodexMediaUnderstandingProvider);
		api.registerVideoGenerationProvider(buildOpenAIVideoGenerationProvider());
	}
});
//#endregion
export { openai_default as default };
