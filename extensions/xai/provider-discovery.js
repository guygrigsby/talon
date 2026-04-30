import { p as readProviderEnvValue } from "../../web-search-provider-common-6uOf-Ga-.js";
import "../../provider-web-search-Sr6gyQX4.js";
import { n as resolveFallbackXaiAuth } from "../../tool-auth-shared-CxJHBTg5.js";
//#region extensions/xai/provider-discovery.ts
const PROVIDER_ID = "xai";
function resolveXaiSyntheticAuth(config) {
	const apiKey = resolveFallbackXaiAuth(config)?.apiKey || readProviderEnvValue(["XAI_API_KEY"]);
	return apiKey ? {
		apiKey,
		source: "xAI plugin config",
		mode: "api-key"
	} : void 0;
}
const xaiProviderDiscovery = {
	id: PROVIDER_ID,
	label: "xAI",
	docsPath: "/providers/models",
	auth: [],
	resolveSyntheticAuth: ({ config }) => resolveXaiSyntheticAuth(config)
};
//#endregion
export { xaiProviderDiscovery as default, xaiProviderDiscovery };
