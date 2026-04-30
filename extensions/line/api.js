import { n as lineChannelPluginCommon, t as linePlugin } from "../../channel-B6A4uktQ.js";
import { n as lineSetupAdapter, t as lineSetupWizard } from "../../setup-surface-DqvS2D0d.js";
//#region extensions/line/src/channel.setup.ts
const lineSetupPlugin = {
	id: "line",
	...lineChannelPluginCommon,
	setupWizard: lineSetupWizard,
	setup: lineSetupAdapter
};
//#endregion
export { linePlugin, lineSetupPlugin };
