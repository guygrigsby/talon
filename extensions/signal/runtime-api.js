import { l as normalizeE164 } from "../../utils-BYAfe1GS.js";
import { t as formatDocsLink } from "../../links-waEZMLI9.js";
import { t as formatCliCommand } from "../../command-format-CgOiLUfR.js";
import { r as buildChannelConfigSchema } from "../../config-schema-D4PhJFTs.js";
import { n as normalizeAccountId, t as DEFAULT_ACCOUNT_ID } from "../../account-id-B6sv0G6z.js";
import { a as SignalConfigSchema } from "../../zod-schema.providers-core-DHQgc0RJ.js";
import { a as chunkText } from "../../chunk-BaeIUMAS.js";
import { n as deleteAccountFromConfigSection, r as setAccountEnabledInConfigSection } from "../../config-helpers-DnaGOHk4.js";
import { n as formatPairingApproveHint } from "../../helpers-CqviAPwR.js";
import "../../text-runtime-BSzvojqX.js";
import { n as emptyPluginConfigSchema } from "../../config-schema-BPkXYvm1.js";
import { s as migrateBaseNameToDefaultAccount, t as applyAccountNameToChannelSection } from "../../setup-helpers-DueR_siR.js";
import { c as getChatChannelMeta } from "../../core-BlBkGFuH.js";
import { t as createPluginRuntimeStore } from "../../runtime-store-lqPxyDlz.js";
import { n as resolveAllowlistProviderRuntimeGroupPolicy, r as resolveDefaultGroupPolicy } from "../../runtime-group-policy-Cpo0ntsM.js";
import { t as resolveChannelMediaMaxBytes } from "../../media-limits-C8Vza2iY.js";
import { t as PAIRING_APPROVED_MESSAGE } from "../../pairing-message-BOzcWnv8.js";
import { c as collectStatusIssuesFromLastError, d as createDefaultChannelRuntimeState, n as buildBaseChannelStatusSummary, t as buildBaseAccountStatusSnapshot } from "../../status-helpers-DQyL9SKw.js";
import { t as detectBinary } from "../../detect-binary-C4SZ-3Kw.js";
import "../../setup-tools-Brk7WeIp.js";
import "../../config-runtime-CLgBc5VJ.js";
import "../../reply-runtime-BV-MCtG5.js";
import "../../media-runtime-BZ5GVHm9.js";
import "../../channel-status-CQsIUMJl.js";
import { i as resolveSignalAccount, n as listSignalAccountIds, r as resolveDefaultSignalAccountId, t as listEnabledSignalAccounts } from "../../accounts-BsTv6e7N.js";
import { d as looksLikeSignalTargetId, f as normalizeSignalMessagingTarget } from "../../identity-CI8N5qTc.js";
import { n as sendReactionSignal, t as removeReactionSignal } from "../../reaction-runtime-api-Bkse_9Fr.js";
import { n as resolveSignalReactionLevel, t as signalMessageActions } from "../../message-actions-D_h9OS9T.js";
import "../../config-api-DDY9xsEU.js";
import { n as installSignalCli } from "../../install-signal-cli-Cgp9p7ne.js";
import { t as monitorSignalProvider } from "../../monitor-g0EOyF2k.js";
import { t as sendMessageSignal } from "../../send-tKdTGsnm.js";
import { t as probeSignal } from "../../probe-BhbVZT-1.js";
//#region extensions/signal/src/runtime.ts
const { setRuntime: setSignalRuntime, clearRuntime: clearSignalRuntime, getRuntime: getSignalRuntime } = createPluginRuntimeStore({
	pluginId: "signal",
	errorMessage: "Signal runtime not initialized"
});
//#endregion
export { DEFAULT_ACCOUNT_ID, PAIRING_APPROVED_MESSAGE, SignalConfigSchema, applyAccountNameToChannelSection, buildBaseAccountStatusSnapshot, buildBaseChannelStatusSummary, buildChannelConfigSchema, chunkText, collectStatusIssuesFromLastError, createDefaultChannelRuntimeState, deleteAccountFromConfigSection, detectBinary, emptyPluginConfigSchema, formatCliCommand, formatDocsLink, formatPairingApproveHint, getChatChannelMeta, installSignalCli, listEnabledSignalAccounts, listSignalAccountIds, looksLikeSignalTargetId, migrateBaseNameToDefaultAccount, monitorSignalProvider, normalizeAccountId, normalizeE164, normalizeSignalMessagingTarget, probeSignal, removeReactionSignal, resolveAllowlistProviderRuntimeGroupPolicy, resolveChannelMediaMaxBytes, resolveDefaultGroupPolicy, resolveDefaultSignalAccountId, resolveSignalAccount, resolveSignalReactionLevel, sendMessageSignal, sendReactionSignal, setAccountEnabledInConfigSection, setSignalRuntime, signalMessageActions };
