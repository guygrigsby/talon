import { r as buildChannelConfigSchema } from "../../config-schema-D4PhJFTs.js";
import { t as DEFAULT_ACCOUNT_ID } from "../../account-id-B6sv0G6z.js";
import { r as IMessageConfigSchema } from "../../zod-schema.providers-core-DHQgc0RJ.js";
import { p as formatTrimmedAllowFromEntries } from "../../channel-config-helpers-CaF4UiF2.js";
import { c as getChatChannelMeta } from "../../core-BlBkGFuH.js";
import { t as createPluginRuntimeStore } from "../../runtime-store-lqPxyDlz.js";
import { t as resolveChannelMediaMaxBytes } from "../../media-limits-C8Vza2iY.js";
import { t as PAIRING_APPROVED_MESSAGE } from "../../pairing-message-BOzcWnv8.js";
import { c as collectStatusIssuesFromLastError, r as buildComputedAccountStatusSnapshot } from "../../status-helpers-DQyL9SKw.js";
import "../../media-runtime-BZ5GVHm9.js";
import { t as chunkTextForOutbound } from "../../text-chunking-BhSjgcaj.js";
import "../../channel-status-CQsIUMJl.js";
import { f as looksLikeIMessageTargetId, h as resolveIMessageConfigDefaultTo, m as resolveIMessageConfigAllowFrom, p as normalizeIMessageMessagingTarget } from "../../conversation-id-BXv6CP-6.js";
import { n as resolveIMessageGroupToolPolicy, t as resolveIMessageGroupRequireMention } from "../../group-policy-fji5j6rY.js";
import "../../config-api-DJXDKASP.js";
import { t as probeIMessage } from "../../probe-DfECES7K.js";
import { n as sendMessageIMessage, t as monitorIMessageProvider } from "../../monitor-cPRDdsuu.js";
//#region extensions/imessage/src/runtime.ts
const { setRuntime: setIMessageRuntime, getRuntime: getIMessageRuntime } = createPluginRuntimeStore({
	pluginId: "imessage",
	errorMessage: "iMessage runtime not initialized"
});
//#endregion
export { DEFAULT_ACCOUNT_ID, IMessageConfigSchema, PAIRING_APPROVED_MESSAGE, buildChannelConfigSchema, buildComputedAccountStatusSnapshot, chunkTextForOutbound, collectStatusIssuesFromLastError, formatTrimmedAllowFromEntries, getChatChannelMeta, looksLikeIMessageTargetId, monitorIMessageProvider, normalizeIMessageMessagingTarget, probeIMessage, resolveChannelMediaMaxBytes, resolveIMessageConfigAllowFrom, resolveIMessageConfigDefaultTo, resolveIMessageGroupRequireMention, resolveIMessageGroupToolPolicy, sendMessageIMessage, setIMessageRuntime };
