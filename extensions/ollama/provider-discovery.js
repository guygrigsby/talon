import { r as buildOllamaProvider } from "../../provider-models-B-qzB0rX.js";
import { i as resolveOllamaDiscoveryResult, n as OLLAMA_PROVIDER_ID, r as hasMeaningfulExplicitOllamaConfig, t as OLLAMA_DEFAULT_API_KEY } from "../../discovery-shared-ByvxA7g4.js";
//#region extensions/ollama/provider-discovery.ts
function resolveOllamaPluginConfig(ctx) {
	return (ctx.config.plugins?.entries ?? {}).ollama?.config ?? {};
}
async function runOllamaDiscovery(ctx) {
	return await resolveOllamaDiscoveryResult({
		ctx,
		pluginConfig: resolveOllamaPluginConfig(ctx),
		buildProvider: buildOllamaProvider
	});
}
const ollamaProviderDiscovery = {
	id: OLLAMA_PROVIDER_ID,
	label: "Ollama",
	docsPath: "/providers/ollama",
	envVars: ["OLLAMA_API_KEY"],
	auth: [],
	resolveSyntheticAuth: ({ providerConfig }) => {
		if (!hasMeaningfulExplicitOllamaConfig(providerConfig)) return;
		return {
			apiKey: OLLAMA_DEFAULT_API_KEY,
			source: "models.providers.ollama (synthetic local key)",
			mode: "api-key"
		};
	},
	discovery: {
		order: "late",
		run: runOllamaDiscovery
	}
};
//#endregion
export { ollamaProviderDiscovery as default, ollamaProviderDiscovery };
