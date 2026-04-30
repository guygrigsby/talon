import { t as definePluginEntry } from "../../plugin-entry-BpVWBiQw.js";
//#region extensions/browser/cli-metadata.ts
var cli_metadata_default = definePluginEntry({
	id: "browser",
	name: "Browser",
	description: "Default browser tool plugin",
	register(api) {
		api.registerCli(async ({ program }) => {
			const { registerBrowserCli } = await import("../../browser-cli-fdhYVv3o.js");
			registerBrowserCli(program);
		}, { commands: ["browser"] });
	}
});
//#endregion
export { cli_metadata_default as default };
