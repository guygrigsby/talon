import { r as redactSensitiveText } from "../../redact-DGGawMpx.js";
import { f as isValidDiagnosticSpanId, m as isValidDiagnosticTraceId, p as isValidDiagnosticTraceFlags } from "../../diagnostic-events-BxOr0dAH.js";
import { t as definePluginEntry } from "../../plugin-entry-BpVWBiQw.js";
import "../../api-BqyqC7YM.js";
import { SpanStatusCode, TraceFlags, context, metrics, trace } from "@opentelemetry/api";
import { OTLPLogExporter } from "@opentelemetry/exporter-logs-otlp-proto";
import { OTLPMetricExporter } from "@opentelemetry/exporter-metrics-otlp-proto";
import { OTLPTraceExporter } from "@opentelemetry/exporter-trace-otlp-proto";
import { resourceFromAttributes } from "@opentelemetry/resources";
import { BatchLogRecordProcessor, LoggerProvider } from "@opentelemetry/sdk-logs";
import { PeriodicExportingMetricReader } from "@opentelemetry/sdk-metrics";
import { NodeSDK } from "@opentelemetry/sdk-node";
import { ParentBasedSampler, TraceIdRatioBasedSampler } from "@opentelemetry/sdk-trace-base";
import { ATTR_SERVICE_NAME } from "@opentelemetry/semantic-conventions";
//#region extensions/diagnostics-otel/src/service.ts
const DEFAULT_SERVICE_NAME = "openclaw";
const DROPPED_OTEL_ATTRIBUTE_KEYS = new Set([
	"openclaw.callId",
	"openclaw.parentSpanId",
	"openclaw.runId",
	"openclaw.sessionId",
	"openclaw.sessionKey",
	"openclaw.spanId",
	"openclaw.toolCallId",
	"openclaw.traceId"
]);
const LOW_CARDINALITY_VALUE_RE = /^[A-Za-z0-9_.:-]{1,120}$/u;
const MAX_OTEL_CONTENT_ATTRIBUTE_CHARS = 4 * 1024;
const MAX_OTEL_CONTENT_ARRAY_ITEMS = 16;
const MAX_OTEL_LOG_BODY_CHARS = 4 * 1024;
const MAX_OTEL_LOG_ATTRIBUTE_COUNT = 64;
const MAX_OTEL_LOG_ATTRIBUTE_VALUE_CHARS = 4 * 1024;
const LOG_RECORD_EXPORT_FAILURE_REPORT_INTERVAL_MS = 6e4;
const OTEL_LOG_RAW_ATTRIBUTE_KEY_RE = /^[A-Za-z0-9_.:-]{1,64}$/u;
const OTEL_LOG_ATTRIBUTE_KEY_RE = /^[A-Za-z0-9_.:-]{1,96}$/u;
const BLOCKED_OTEL_LOG_ATTRIBUTE_KEYS = new Set([
	"__proto__",
	"prototype",
	"constructor"
]);
const PRELOADED_OTEL_SDK_ENV = "OPENCLAW_OTEL_PRELOADED";
const OTEL_EXPORTER_OTLP_ENDPOINT_ENV = "OTEL_EXPORTER_OTLP_ENDPOINT";
const OTEL_EXPORTER_OTLP_TRACES_ENDPOINT_ENV = "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT";
const OTEL_EXPORTER_OTLP_METRICS_ENDPOINT_ENV = "OTEL_EXPORTER_OTLP_METRICS_ENDPOINT";
const OTEL_EXPORTER_OTLP_LOGS_ENDPOINT_ENV = "OTEL_EXPORTER_OTLP_LOGS_ENDPOINT";
const OTEL_SEMCONV_STABILITY_OPT_IN_ENV = "OTEL_SEMCONV_STABILITY_OPT_IN";
const GEN_AI_LATEST_EXPERIMENTAL_OPT_IN = "gen_ai_latest_experimental";
const GEN_AI_TOKEN_USAGE_BUCKETS = [
	1,
	4,
	16,
	64,
	256,
	1024,
	4096,
	16384,
	65536,
	262144,
	1048576,
	4194304,
	16777216,
	67108864
];
const GEN_AI_OPERATION_DURATION_BUCKETS = [
	.01,
	.02,
	.04,
	.08,
	.16,
	.32,
	.64,
	1.28,
	2.56,
	5.12,
	10.24,
	20.48,
	40.96,
	81.92
];
const NO_CONTENT_CAPTURE = {
	inputMessages: false,
	outputMessages: false,
	toolInputs: false,
	toolOutputs: false,
	systemPrompt: false
};
function normalizeEndpoint(endpoint) {
	const trimmed = endpoint?.trim();
	return trimmed ? trimmed.replace(/\/+$/, "") : void 0;
}
function resolveOtelUrl(endpoint, path) {
	if (!endpoint) return;
	const endpointWithoutQueryOrFragment = endpoint.split(/[?#]/, 1)[0] ?? endpoint;
	if (/\/v1\/(?:traces|metrics|logs)$/i.test(endpointWithoutQueryOrFragment)) return endpoint;
	return `${endpoint}/${path}`;
}
function resolveSignalOtelUrl(params) {
	return resolveOtelUrl(normalizeEndpoint(params.signalEndpoint ?? params.signalEnvEndpoint) ?? params.endpoint, params.path);
}
function resolveSampleRate(value) {
	if (typeof value !== "number" || !Number.isFinite(value)) return;
	if (value < 0 || value > 1) return;
	return value;
}
function formatError(err) {
	if (err instanceof Error) return err.stack ?? err.message;
	if (typeof err === "string") return err;
	try {
		return JSON.stringify(err);
	} catch {
		return String(err);
	}
}
function errorCategory(err) {
	try {
		if (err instanceof Error && typeof err.name === "string" && err.name.trim()) return lowCardinalityAttr(err.name, "Error");
		return lowCardinalityAttr(typeof err, "unknown");
	} catch {
		return "unknown";
	}
}
function redactOtelAttributes(attributes) {
	const redactedAttributes = {};
	for (const [key, value] of Object.entries(attributes)) {
		if (DROPPED_OTEL_ATTRIBUTE_KEYS.has(key)) continue;
		redactedAttributes[key] = typeof value === "string" ? redactSensitiveText(value) : value;
	}
	return redactedAttributes;
}
function lowCardinalityAttr(value, fallback = "unknown") {
	if (!value) return fallback;
	const redacted = redactSensitiveText(value.trim());
	return LOW_CARDINALITY_VALUE_RE.test(redacted) ? redacted : fallback;
}
function hasOtelSemconvOptIn(value, optIn) {
	return value?.split(",").map((part) => part.trim()).includes(optIn) ?? false;
}
function emitLatestGenAiSemconv() {
	return hasOtelSemconvOptIn(process.env[OTEL_SEMCONV_STABILITY_OPT_IN_ENV], GEN_AI_LATEST_EXPERIMENTAL_OPT_IN);
}
function genAiOperationName(api) {
	const normalized = api?.trim().toLowerCase();
	if (!normalized) return "chat";
	if (normalized === "completions" || normalized.endsWith("-completions")) return "text_completion";
	if (normalized === "generate_content" || normalized.includes("generative-ai")) return "generate_content";
	return "chat";
}
function positiveFiniteNumber(value) {
	return typeof value === "number" && Number.isFinite(value) && value > 0 ? value : void 0;
}
function assignPositiveNumberAttr(attrs, key, value) {
	const normalized = positiveFiniteNumber(value);
	if (normalized !== void 0) attrs[key] = normalized;
}
function assignGenAiSpanIdentityAttrs(attrs, input) {
	if (emitLatestGenAiSemconv()) attrs["gen_ai.provider.name"] = lowCardinalityAttr(input.provider);
	else attrs["gen_ai.system"] = lowCardinalityAttr(input.provider);
	if (input.model) attrs["gen_ai.request.model"] = lowCardinalityAttr(input.model);
	attrs["gen_ai.operation.name"] = genAiOperationName(input.api);
}
function assignGenAiModelCallAttrs(attrs, evt) {
	assignGenAiSpanIdentityAttrs(attrs, evt);
}
function addUpstreamRequestIdSpanEvent(span, upstreamRequestIdHash) {
	if (!upstreamRequestIdHash) return;
	const boundedHash = lowCardinalityAttr(upstreamRequestIdHash);
	if (boundedHash === "unknown") return;
	span.addEvent?.("openclaw.provider.request", { "openclaw.upstreamRequestIdHash": boundedHash });
}
function clampOtelLogText(value, maxChars) {
	return value.length > maxChars ? `${value.slice(0, maxChars)}...(truncated)` : value;
}
function normalizeOtelLogString(value, maxChars) {
	return clampOtelLogText(redactSensitiveText(value), maxChars);
}
function resolveContentCapturePolicy(value) {
	if (value === true) return {
		inputMessages: true,
		outputMessages: true,
		toolInputs: true,
		toolOutputs: true,
		systemPrompt: false
	};
	if (!value || typeof value !== "object" || Array.isArray(value)) return NO_CONTENT_CAPTURE;
	const config = value;
	if (config.enabled !== true) return NO_CONTENT_CAPTURE;
	return {
		inputMessages: config.inputMessages === true,
		outputMessages: config.outputMessages === true,
		toolInputs: config.toolInputs === true,
		toolOutputs: config.toolOutputs === true,
		systemPrompt: config.systemPrompt === true
	};
}
function hasPreloadedOtelSdk() {
	return process.env[PRELOADED_OTEL_SDK_ENV] === "1";
}
function normalizeOtelContentValue(value) {
	if (typeof value === "string") return normalizeOtelLogString(value, MAX_OTEL_CONTENT_ATTRIBUTE_CHARS);
	if (Array.isArray(value)) {
		const items = [];
		for (const item of value.slice(0, MAX_OTEL_CONTENT_ARRAY_ITEMS)) if (typeof item === "string") items.push(item);
		if (items.length > 0) return normalizeOtelLogString(items.join("\n"), MAX_OTEL_CONTENT_ATTRIBUTE_CHARS);
	}
}
function assignOtelContentAttribute(attributes, key, value) {
	const normalized = normalizeOtelContentValue(value);
	if (normalized) attributes[key] = normalized;
}
function assignOtelModelContentAttributes(attributes, event, policy) {
	if (policy.inputMessages) assignOtelContentAttribute(attributes, "openclaw.content.input_messages", event.inputMessages);
	if (policy.outputMessages) assignOtelContentAttribute(attributes, "openclaw.content.output_messages", event.outputMessages);
	if (policy.systemPrompt) assignOtelContentAttribute(attributes, "openclaw.content.system_prompt", event.systemPrompt);
}
function assignOtelToolContentAttributes(attributes, event, policy) {
	if (policy.toolInputs) assignOtelContentAttribute(attributes, "openclaw.content.tool_input", event.toolInput);
	if (policy.toolOutputs) assignOtelContentAttribute(attributes, "openclaw.content.tool_output", event.toolOutput);
}
function assignOtelLogAttribute(attributes, key, value) {
	if (Object.keys(attributes).length >= MAX_OTEL_LOG_ATTRIBUTE_COUNT) return;
	if (BLOCKED_OTEL_LOG_ATTRIBUTE_KEYS.has(key)) return;
	if (redactSensitiveText(key) !== key) return;
	if (!OTEL_LOG_ATTRIBUTE_KEY_RE.test(key)) return;
	if (typeof value === "string") {
		attributes[key] = normalizeOtelLogString(value, MAX_OTEL_LOG_ATTRIBUTE_VALUE_CHARS);
		return;
	}
	if (typeof value === "number" && Number.isFinite(value)) {
		attributes[key] = value;
		return;
	}
	if (typeof value === "boolean") attributes[key] = value;
}
function normalizeTraceContext(value) {
	if (!value || typeof value !== "object" || Array.isArray(value)) return;
	const candidate = value;
	if (!isValidDiagnosticTraceId(candidate.traceId)) return;
	if (candidate.spanId !== void 0 && !isValidDiagnosticSpanId(candidate.spanId)) return;
	if (candidate.parentSpanId !== void 0 && !isValidDiagnosticSpanId(candidate.parentSpanId)) return;
	if (candidate.traceFlags !== void 0 && !isValidDiagnosticTraceFlags(candidate.traceFlags)) return;
	return {
		traceId: candidate.traceId,
		...candidate.spanId ? { spanId: candidate.spanId } : {},
		...candidate.parentSpanId ? { parentSpanId: candidate.parentSpanId } : {},
		...candidate.traceFlags ? { traceFlags: candidate.traceFlags } : {}
	};
}
function assignOtelLogEventAttributes(attributes, eventAttributes) {
	if (!eventAttributes) return;
	for (const rawKey in eventAttributes) {
		if (Object.keys(attributes).length >= MAX_OTEL_LOG_ATTRIBUTE_COUNT) break;
		if (!Object.hasOwn(eventAttributes, rawKey)) continue;
		const key = rawKey.trim();
		if (BLOCKED_OTEL_LOG_ATTRIBUTE_KEYS.has(key)) continue;
		if (redactSensitiveText(key) !== key) continue;
		if (!OTEL_LOG_RAW_ATTRIBUTE_KEY_RE.test(key)) continue;
		assignOtelLogAttribute(attributes, `openclaw.${key}`, eventAttributes[rawKey]);
	}
}
function traceFlagsToOtel(traceFlags) {
	return (Number.parseInt(traceFlags ?? "00", 16) & TraceFlags.SAMPLED) !== 0 ? TraceFlags.SAMPLED : TraceFlags.NONE;
}
function contextForTraceContext(traceContext) {
	const normalized = normalizeTraceContext(traceContext);
	if (!normalized?.spanId) return;
	return trace.setSpanContext(context.active(), {
		traceId: normalized.traceId,
		spanId: normalized.spanId,
		traceFlags: traceFlagsToOtel(normalized.traceFlags),
		isRemote: true
	});
}
function contextForDiagnosticSpanParent(traceContext) {
	const normalized = normalizeTraceContext(traceContext);
	if (!normalized?.parentSpanId) return;
	return trace.setSpanContext(context.active(), {
		traceId: normalized.traceId,
		spanId: normalized.parentSpanId,
		traceFlags: traceFlagsToOtel(normalized.traceFlags),
		isRemote: true
	});
}
function contextForTrustedTraceContext(evt, metadata) {
	return metadata.trusted ? contextForTraceContext(evt.trace) : void 0;
}
function contextForTrustedDiagnosticSpanParent(evt, metadata) {
	return metadata.trusted ? contextForDiagnosticSpanParent(evt.trace) : void 0;
}
function addTraceAttributes(attributes, traceContext) {
	const normalized = normalizeTraceContext(traceContext);
	if (!normalized) return;
	attributes["openclaw.traceId"] = normalized.traceId;
	if (normalized.spanId) attributes["openclaw.spanId"] = normalized.spanId;
	if (normalized.parentSpanId) attributes["openclaw.parentSpanId"] = normalized.parentSpanId;
	if (normalized.traceFlags) attributes["openclaw.traceFlags"] = normalized.traceFlags;
}
function createDiagnosticsOtelService() {
	let sdk = null;
	let logProvider = null;
	let unsubscribe = null;
	const stopStarted = async () => {
		const currentUnsubscribe = unsubscribe;
		const currentLogProvider = logProvider;
		const currentSdk = sdk;
		unsubscribe = null;
		logProvider = null;
		sdk = null;
		currentUnsubscribe?.();
		if (currentLogProvider) await currentLogProvider.shutdown().catch(() => void 0);
		if (currentSdk) await currentSdk.shutdown().catch(() => void 0);
	};
	return {
		id: "diagnostics-otel",
		async start(ctx) {
			await stopStarted();
			const cfg = ctx.config.diagnostics;
			const otel = cfg?.otel;
			if (!cfg?.enabled || !otel?.enabled) return;
			const emitExporterEvent = (event) => {
				try {
					ctx.internalDiagnostics?.emit({
						type: "telemetry.exporter",
						...event
					});
				} catch {}
			};
			const emitForSignals = (signals, event) => {
				for (const signal of signals) emitExporterEvent({
					signal,
					...event
				});
			};
			const tracesEnabled = otel.traces !== false;
			const metricsEnabled = otel.metrics !== false;
			const logsEnabled = otel.logs === true;
			const enabledSignals = [
				...tracesEnabled ? ["traces"] : [],
				...metricsEnabled ? ["metrics"] : [],
				...logsEnabled ? ["logs"] : []
			];
			if (enabledSignals.length === 0) return;
			const protocol = otel.protocol ?? process.env.OTEL_EXPORTER_OTLP_PROTOCOL ?? "http/protobuf";
			if (protocol !== "http/protobuf") {
				emitForSignals(enabledSignals, {
					exporter: "diagnostics-otel",
					status: "failure",
					reason: "unsupported_protocol"
				});
				ctx.logger.warn(`diagnostics-otel: unsupported protocol ${protocol}`);
				return;
			}
			const endpoint = normalizeEndpoint(otel.endpoint ?? process.env[OTEL_EXPORTER_OTLP_ENDPOINT_ENV]);
			const headers = otel.headers ?? void 0;
			const serviceName = otel.serviceName?.trim() || process.env.OTEL_SERVICE_NAME || DEFAULT_SERVICE_NAME;
			const sampleRate = resolveSampleRate(otel.sampleRate);
			const contentCapturePolicy = resolveContentCapturePolicy(otel.captureContent);
			const sdkPreloaded = hasPreloadedOtelSdk();
			const resource = resourceFromAttributes({ [ATTR_SERVICE_NAME]: serviceName });
			const logUrl = resolveSignalOtelUrl({
				signalEndpoint: otel.logsEndpoint,
				signalEnvEndpoint: process.env[OTEL_EXPORTER_OTLP_LOGS_ENDPOINT_ENV],
				endpoint,
				path: "v1/logs"
			});
			if (!sdkPreloaded && (tracesEnabled || metricsEnabled)) {
				const traceUrl = resolveSignalOtelUrl({
					signalEndpoint: otel.tracesEndpoint,
					signalEnvEndpoint: process.env[OTEL_EXPORTER_OTLP_TRACES_ENDPOINT_ENV],
					endpoint,
					path: "v1/traces"
				});
				const metricUrl = resolveSignalOtelUrl({
					signalEndpoint: otel.metricsEndpoint,
					signalEnvEndpoint: process.env[OTEL_EXPORTER_OTLP_METRICS_ENDPOINT_ENV],
					endpoint,
					path: "v1/metrics"
				});
				const traceExporter = tracesEnabled ? new OTLPTraceExporter({
					...traceUrl ? { url: traceUrl } : {},
					...headers ? { headers } : {}
				}) : void 0;
				const metricExporter = metricsEnabled ? new OTLPMetricExporter({
					...metricUrl ? { url: metricUrl } : {},
					...headers ? { headers } : {}
				}) : void 0;
				const metricReader = metricExporter ? new PeriodicExportingMetricReader({
					exporter: metricExporter,
					...typeof otel.flushIntervalMs === "number" ? { exportIntervalMillis: Math.max(1e3, otel.flushIntervalMs) } : {}
				}) : void 0;
				sdk = new NodeSDK({
					resource,
					...traceExporter ? { traceExporter } : {},
					...metricReader ? { metricReader } : {},
					...sampleRate !== void 0 ? { sampler: new ParentBasedSampler({ root: new TraceIdRatioBasedSampler(sampleRate) }) } : {}
				});
				try {
					sdk.start();
				} catch (err) {
					emitForSignals([...tracesEnabled ? ["traces"] : [], ...metricsEnabled ? ["metrics"] : []], {
						exporter: "diagnostics-otel",
						status: "failure",
						reason: "start_failed",
						errorCategory: errorCategory(err)
					});
					await stopStarted();
					ctx.logger.error(`diagnostics-otel: failed to start SDK: ${formatError(err)}`);
					throw err;
				}
			} else if (sdkPreloaded && (tracesEnabled || metricsEnabled)) ctx.logger.info("diagnostics-otel: using preloaded OpenTelemetry SDK");
			const logSeverityMap = {
				TRACE: 1,
				DEBUG: 5,
				INFO: 9,
				WARN: 13,
				ERROR: 17,
				FATAL: 21
			};
			const meter = metrics.getMeter("openclaw");
			const tracer = trace.getTracer("openclaw");
			const tokensCounter = meter.createCounter("openclaw.tokens", {
				unit: "1",
				description: "Token usage by type"
			});
			const genAiTokenUsageHistogram = meter.createHistogram("gen_ai.client.token.usage", {
				unit: "{token}",
				description: "Number of input and output tokens used by GenAI client operations",
				advice: { explicitBucketBoundaries: GEN_AI_TOKEN_USAGE_BUCKETS }
			});
			const genAiOperationDurationHistogram = meter.createHistogram("gen_ai.client.operation.duration", {
				unit: "s",
				description: "GenAI client operation duration",
				advice: { explicitBucketBoundaries: GEN_AI_OPERATION_DURATION_BUCKETS }
			});
			const costCounter = meter.createCounter("openclaw.cost.usd", {
				unit: "1",
				description: "Estimated model cost (USD)"
			});
			const durationHistogram = meter.createHistogram("openclaw.run.duration_ms", {
				unit: "ms",
				description: "Agent run duration"
			});
			const harnessDurationHistogram = meter.createHistogram("openclaw.harness.duration_ms", {
				unit: "ms",
				description: "Agent harness lifecycle duration"
			});
			const contextHistogram = meter.createHistogram("openclaw.context.tokens", {
				unit: "1",
				description: "Context window size and usage"
			});
			const webhookReceivedCounter = meter.createCounter("openclaw.webhook.received", {
				unit: "1",
				description: "Webhook requests received"
			});
			const webhookErrorCounter = meter.createCounter("openclaw.webhook.error", {
				unit: "1",
				description: "Webhook processing errors"
			});
			const webhookDurationHistogram = meter.createHistogram("openclaw.webhook.duration_ms", {
				unit: "ms",
				description: "Webhook processing duration"
			});
			const messageQueuedCounter = meter.createCounter("openclaw.message.queued", {
				unit: "1",
				description: "Messages queued for processing"
			});
			const messageProcessedCounter = meter.createCounter("openclaw.message.processed", {
				unit: "1",
				description: "Messages processed by outcome"
			});
			const messageDurationHistogram = meter.createHistogram("openclaw.message.duration_ms", {
				unit: "ms",
				description: "Message processing duration"
			});
			const messageDeliveryStartedCounter = meter.createCounter("openclaw.message.delivery.started", {
				unit: "1",
				description: "Outbound message delivery attempts started"
			});
			const messageDeliveryDurationHistogram = meter.createHistogram("openclaw.message.delivery.duration_ms", {
				unit: "ms",
				description: "Outbound message delivery duration"
			});
			const queueDepthHistogram = meter.createHistogram("openclaw.queue.depth", {
				unit: "1",
				description: "Queue depth on enqueue/dequeue"
			});
			const queueWaitHistogram = meter.createHistogram("openclaw.queue.wait_ms", {
				unit: "ms",
				description: "Queue wait time before execution"
			});
			const laneEnqueueCounter = meter.createCounter("openclaw.queue.lane.enqueue", {
				unit: "1",
				description: "Command queue lane enqueue events"
			});
			const laneDequeueCounter = meter.createCounter("openclaw.queue.lane.dequeue", {
				unit: "1",
				description: "Command queue lane dequeue events"
			});
			const sessionStateCounter = meter.createCounter("openclaw.session.state", {
				unit: "1",
				description: "Session state transitions"
			});
			const sessionStuckCounter = meter.createCounter("openclaw.session.stuck", {
				unit: "1",
				description: "Sessions stuck in processing"
			});
			const sessionStuckAgeHistogram = meter.createHistogram("openclaw.session.stuck_age_ms", {
				unit: "ms",
				description: "Age of stuck sessions"
			});
			const runAttemptCounter = meter.createCounter("openclaw.run.attempt", {
				unit: "1",
				description: "Run attempts"
			});
			const toolLoopCounter = meter.createCounter("openclaw.tool.loop", {
				unit: "1",
				description: "Detected repetitive tool-call loop events"
			});
			const modelCallDurationHistogram = meter.createHistogram("openclaw.model_call.duration_ms", {
				unit: "ms",
				description: "Model call duration"
			});
			const toolExecutionDurationHistogram = meter.createHistogram("openclaw.tool.execution.duration_ms", {
				unit: "ms",
				description: "Tool execution duration"
			});
			const execProcessDurationHistogram = meter.createHistogram("openclaw.exec.duration_ms", {
				unit: "ms",
				description: "Exec process duration"
			});
			const memoryRssHistogram = meter.createHistogram("openclaw.memory.rss_bytes", {
				unit: "By",
				description: "Resident set size reported by diagnostic memory samples"
			});
			const memoryHeapUsedHistogram = meter.createHistogram("openclaw.memory.heap_used_bytes", {
				unit: "By",
				description: "Heap used bytes reported by diagnostic memory samples"
			});
			const memoryHeapTotalHistogram = meter.createHistogram("openclaw.memory.heap_total_bytes", {
				unit: "By",
				description: "Heap total bytes reported by diagnostic memory samples"
			});
			const memoryExternalHistogram = meter.createHistogram("openclaw.memory.external_bytes", {
				unit: "By",
				description: "External memory bytes reported by diagnostic memory samples"
			});
			const memoryArrayBuffersHistogram = meter.createHistogram("openclaw.memory.array_buffers_bytes", {
				unit: "By",
				description: "ArrayBuffer bytes reported by diagnostic memory samples"
			});
			const memoryPressureCounter = meter.createCounter("openclaw.memory.pressure", {
				unit: "1",
				description: "Diagnostic memory pressure events"
			});
			const telemetryExporterCounter = meter.createCounter("openclaw.telemetry.exporter.events", {
				unit: "1",
				description: "Diagnostic telemetry exporter lifecycle and failure events"
			});
			let recordLogRecord;
			if (logsEnabled) {
				let logRecordExportFailureLastReportedAt = Number.NEGATIVE_INFINITY;
				logProvider = new LoggerProvider({
					resource,
					processors: [new BatchLogRecordProcessor(new OTLPLogExporter({
						...logUrl ? { url: logUrl } : {},
						...headers ? { headers } : {}
					}), typeof otel.flushIntervalMs === "number" ? { scheduledDelayMillis: Math.max(1e3, otel.flushIntervalMs) } : {})]
				});
				const otelLogger = logProvider.getLogger("openclaw");
				recordLogRecord = (evt, metadata) => {
					try {
						const logLevelName = evt.level || "INFO";
						const severityNumber = logSeverityMap[logLevelName] ?? 9;
						const attributes = Object.create(null);
						assignOtelLogAttribute(attributes, "openclaw.log.level", logLevelName);
						if (evt.loggerName) assignOtelLogAttribute(attributes, "openclaw.logger", evt.loggerName);
						if (evt.loggerParents?.length) assignOtelLogAttribute(attributes, "openclaw.logger.parents", evt.loggerParents.join("."));
						assignOtelLogEventAttributes(attributes, evt.attributes);
						if (evt.code?.line) assignOtelLogAttribute(attributes, "code.lineno", evt.code.line);
						if (evt.code?.functionName) assignOtelLogAttribute(attributes, "code.function", evt.code.functionName);
						if (metadata.trusted) addTraceAttributes(attributes, evt.trace);
						const logRecord = {
							body: normalizeOtelLogString(evt.message || "log", MAX_OTEL_LOG_BODY_CHARS),
							severityText: logLevelName,
							severityNumber,
							attributes: redactOtelAttributes(attributes),
							timestamp: evt.ts
						};
						const logContext = contextForTrustedTraceContext(evt, metadata);
						if (logContext) logRecord.context = logContext;
						otelLogger.emit(logRecord);
					} catch (err) {
						emitExporterEvent({
							exporter: "diagnostics-otel",
							signal: "logs",
							status: "failure",
							reason: "emit_failed",
							errorCategory: errorCategory(err)
						});
						const now = Date.now();
						if (now - logRecordExportFailureLastReportedAt >= LOG_RECORD_EXPORT_FAILURE_REPORT_INTERVAL_MS) {
							logRecordExportFailureLastReportedAt = now;
							ctx.logger.error(`diagnostics-otel: log record export failed: ${formatError(err)}`);
						}
					}
				};
			}
			const spanWithDuration = (name, attributes, durationMs, options = {}) => {
				const endTimeMs = options.endTimeMs ?? Date.now();
				const startTime = typeof durationMs === "number" ? endTimeMs - Math.max(0, durationMs) : void 0;
				const parentContext = "parentContext" in options ? options.parentContext ?? void 0 : void 0;
				return tracer.startSpan(name, {
					attributes: redactOtelAttributes(attributes),
					...startTime !== void 0 ? { startTime } : {}
				}, parentContext);
			};
			const addRunAttrs = (spanAttrs, evt) => {
				if (evt.provider) spanAttrs["openclaw.provider"] = evt.provider;
				if (evt.model) spanAttrs["openclaw.model"] = evt.model;
				if (evt.channel) spanAttrs["openclaw.channel"] = evt.channel;
				if (evt.trigger) spanAttrs["openclaw.trigger"] = evt.trigger;
			};
			const paramsSummaryAttrs = (summary) => {
				if (!summary) return {};
				return {
					"openclaw.tool.params.kind": summary.kind,
					..."length" in summary ? { "openclaw.tool.params.length": summary.length } : {}
				};
			};
			const recordModelUsage = (evt, metadata) => {
				const attrs = {
					"openclaw.channel": evt.channel ?? "unknown",
					"openclaw.agent": lowCardinalityAttr(evt.agentId),
					"openclaw.provider": evt.provider ?? "unknown",
					"openclaw.model": evt.model ?? "unknown"
				};
				const genAiAttrs = {
					"gen_ai.operation.name": "chat",
					"gen_ai.provider.name": lowCardinalityAttr(evt.provider),
					"gen_ai.request.model": lowCardinalityAttr(evt.model)
				};
				const usage = evt.usage;
				if (usage.input) {
					tokensCounter.add(usage.input, {
						...attrs,
						"openclaw.token": "input"
					});
					genAiTokenUsageHistogram.record(usage.input, {
						...genAiAttrs,
						"gen_ai.token.type": "input"
					});
				}
				if (usage.output) {
					tokensCounter.add(usage.output, {
						...attrs,
						"openclaw.token": "output"
					});
					genAiTokenUsageHistogram.record(usage.output, {
						...genAiAttrs,
						"gen_ai.token.type": "output"
					});
				}
				if (usage.cacheRead) tokensCounter.add(usage.cacheRead, {
					...attrs,
					"openclaw.token": "cache_read"
				});
				if (usage.cacheWrite) tokensCounter.add(usage.cacheWrite, {
					...attrs,
					"openclaw.token": "cache_write"
				});
				if (usage.promptTokens) tokensCounter.add(usage.promptTokens, {
					...attrs,
					"openclaw.token": "prompt"
				});
				if (usage.total) tokensCounter.add(usage.total, {
					...attrs,
					"openclaw.token": "total"
				});
				if (evt.costUsd) costCounter.add(evt.costUsd, attrs);
				if (evt.durationMs) durationHistogram.record(evt.durationMs, attrs);
				if (evt.context?.limit) contextHistogram.record(evt.context.limit, {
					...attrs,
					"openclaw.context": "limit"
				});
				if (evt.context?.used) contextHistogram.record(evt.context.used, {
					...attrs,
					"openclaw.context": "used"
				});
				if (!tracesEnabled) return;
				const genAiInputTokens = usage.promptTokens ?? (usage.input ?? 0) + (usage.cacheRead ?? 0) + (usage.cacheWrite ?? 0);
				const spanAttrs = {
					...attrs,
					"openclaw.tokens.input": usage.input ?? 0,
					"openclaw.tokens.output": usage.output ?? 0,
					"openclaw.tokens.cache_read": usage.cacheRead ?? 0,
					"openclaw.tokens.cache_write": usage.cacheWrite ?? 0,
					"openclaw.tokens.total": usage.total ?? 0
				};
				assignGenAiSpanIdentityAttrs(spanAttrs, evt);
				assignPositiveNumberAttr(spanAttrs, "gen_ai.usage.input_tokens", genAiInputTokens);
				assignPositiveNumberAttr(spanAttrs, "gen_ai.usage.output_tokens", usage.output);
				assignPositiveNumberAttr(spanAttrs, "gen_ai.usage.cache_read.input_tokens", usage.cacheRead);
				assignPositiveNumberAttr(spanAttrs, "gen_ai.usage.cache_creation.input_tokens", usage.cacheWrite);
				spanWithDuration("openclaw.model.usage", spanAttrs, evt.durationMs, {
					parentContext: contextForTrustedDiagnosticSpanParent(evt, metadata),
					endTimeMs: evt.ts
				}).end(evt.ts);
			};
			const recordWebhookReceived = (evt) => {
				const attrs = {
					"openclaw.channel": evt.channel ?? "unknown",
					"openclaw.webhook": evt.updateType ?? "unknown"
				};
				webhookReceivedCounter.add(1, attrs);
			};
			const recordWebhookProcessed = (evt) => {
				const attrs = {
					"openclaw.channel": evt.channel ?? "unknown",
					"openclaw.webhook": evt.updateType ?? "unknown"
				};
				if (typeof evt.durationMs === "number") webhookDurationHistogram.record(evt.durationMs, attrs);
				if (!tracesEnabled) return;
				const spanAttrs = { ...attrs };
				if (evt.chatId !== void 0) spanAttrs["openclaw.chatId"] = String(evt.chatId);
				spanWithDuration("openclaw.webhook.processed", spanAttrs, evt.durationMs).end();
			};
			const recordWebhookError = (evt) => {
				const attrs = {
					"openclaw.channel": evt.channel ?? "unknown",
					"openclaw.webhook": evt.updateType ?? "unknown"
				};
				webhookErrorCounter.add(1, attrs);
				if (!tracesEnabled) return;
				const redactedError = redactSensitiveText(evt.error);
				const spanAttrs = {
					...attrs,
					"openclaw.error": redactedError
				};
				if (evt.chatId !== void 0) spanAttrs["openclaw.chatId"] = String(evt.chatId);
				const span = tracer.startSpan("openclaw.webhook.error", { attributes: spanAttrs });
				span.setStatus({
					code: SpanStatusCode.ERROR,
					message: redactedError
				});
				span.end();
			};
			const recordMessageQueued = (evt) => {
				const attrs = {
					"openclaw.channel": evt.channel ?? "unknown",
					"openclaw.source": evt.source ?? "unknown"
				};
				messageQueuedCounter.add(1, attrs);
				if (typeof evt.queueDepth === "number") queueDepthHistogram.record(evt.queueDepth, attrs);
			};
			const recordMessageProcessed = (evt) => {
				const attrs = {
					"openclaw.channel": evt.channel ?? "unknown",
					"openclaw.outcome": evt.outcome ?? "unknown"
				};
				messageProcessedCounter.add(1, attrs);
				if (typeof evt.durationMs === "number") messageDurationHistogram.record(evt.durationMs, attrs);
				if (!tracesEnabled) return;
				const spanAttrs = { ...attrs };
				if (evt.chatId !== void 0) spanAttrs["openclaw.chatId"] = String(evt.chatId);
				if (evt.messageId !== void 0) spanAttrs["openclaw.messageId"] = String(evt.messageId);
				if (evt.reason) spanAttrs["openclaw.reason"] = redactSensitiveText(evt.reason);
				const span = spanWithDuration("openclaw.message.processed", spanAttrs, evt.durationMs);
				if (evt.outcome === "error" && evt.error) span.setStatus({
					code: SpanStatusCode.ERROR,
					message: redactSensitiveText(evt.error)
				});
				span.end();
			};
			const messageDeliveryAttrs = (evt) => ({
				"openclaw.channel": evt.channel,
				"openclaw.delivery.kind": evt.deliveryKind
			});
			const recordMessageDeliveryStarted = (evt) => {
				messageDeliveryStartedCounter.add(1, messageDeliveryAttrs(evt));
			};
			const recordMessageDeliveryCompleted = (evt) => {
				const attrs = {
					...messageDeliveryAttrs(evt),
					"openclaw.outcome": "completed"
				};
				messageDeliveryDurationHistogram.record(evt.durationMs, attrs);
				if (!tracesEnabled) return;
				spanWithDuration("openclaw.message.delivery", {
					...attrs,
					"openclaw.delivery.result_count": evt.resultCount
				}, evt.durationMs, { endTimeMs: evt.ts }).end(evt.ts);
			};
			const recordMessageDeliveryError = (evt) => {
				const attrs = {
					...messageDeliveryAttrs(evt),
					"openclaw.outcome": "error",
					"openclaw.errorCategory": lowCardinalityAttr(evt.errorCategory, "other")
				};
				messageDeliveryDurationHistogram.record(evt.durationMs, attrs);
				if (!tracesEnabled) return;
				const span = spanWithDuration("openclaw.message.delivery", attrs, evt.durationMs, { endTimeMs: evt.ts });
				span.setStatus({
					code: SpanStatusCode.ERROR,
					message: redactSensitiveText(evt.errorCategory)
				});
				span.end(evt.ts);
			};
			const recordLaneEnqueue = (evt) => {
				const attrs = { "openclaw.lane": evt.lane };
				laneEnqueueCounter.add(1, attrs);
				queueDepthHistogram.record(evt.queueSize, attrs);
			};
			const recordLaneDequeue = (evt) => {
				const attrs = { "openclaw.lane": evt.lane };
				laneDequeueCounter.add(1, attrs);
				queueDepthHistogram.record(evt.queueSize, attrs);
				if (typeof evt.waitMs === "number") queueWaitHistogram.record(evt.waitMs, attrs);
			};
			const recordSessionState = (evt) => {
				const attrs = { "openclaw.state": evt.state };
				if (evt.reason) attrs["openclaw.reason"] = redactSensitiveText(evt.reason);
				sessionStateCounter.add(1, attrs);
			};
			const recordSessionStuck = (evt) => {
				const attrs = { "openclaw.state": evt.state };
				sessionStuckCounter.add(1, attrs);
				if (typeof evt.ageMs === "number") sessionStuckAgeHistogram.record(evt.ageMs, attrs);
				if (!tracesEnabled) return;
				const spanAttrs = { ...attrs };
				spanAttrs["openclaw.queueDepth"] = evt.queueDepth ?? 0;
				spanAttrs["openclaw.ageMs"] = evt.ageMs;
				const span = tracer.startSpan("openclaw.session.stuck", { attributes: spanAttrs });
				span.setStatus({
					code: SpanStatusCode.ERROR,
					message: "session stuck"
				});
				span.end();
			};
			const recordRunAttempt = (evt) => {
				runAttemptCounter.add(1, { "openclaw.attempt": evt.attempt });
			};
			const toolLoopAttrs = (evt) => ({
				"openclaw.toolName": lowCardinalityAttr(evt.toolName, "tool"),
				"openclaw.loop.level": evt.level,
				"openclaw.loop.action": evt.action,
				"openclaw.loop.detector": evt.detector,
				"openclaw.loop.count": evt.count,
				...evt.pairedToolName ? { "openclaw.loop.paired_tool": lowCardinalityAttr(evt.pairedToolName, "tool") } : {}
			});
			const recordToolLoop = (evt) => {
				const attrs = toolLoopAttrs(evt);
				toolLoopCounter.add(1, attrs);
				if (!tracesEnabled) return;
				const span = spanWithDuration("openclaw.tool.loop", attrs, 0, { endTimeMs: evt.ts });
				if (evt.level === "critical" || evt.action === "block") span.setStatus({
					code: SpanStatusCode.ERROR,
					message: `${evt.detector}:${evt.action}`
				});
				span.end(evt.ts);
			};
			const recordMemoryUsageMetrics = (evt, attrs = {}) => {
				memoryRssHistogram.record(evt.memory.rssBytes, attrs);
				memoryHeapUsedHistogram.record(evt.memory.heapUsedBytes, attrs);
				memoryHeapTotalHistogram.record(evt.memory.heapTotalBytes, attrs);
				memoryExternalHistogram.record(evt.memory.externalBytes, attrs);
				memoryArrayBuffersHistogram.record(evt.memory.arrayBuffersBytes, attrs);
			};
			const recordMemorySample = (evt) => {
				recordMemoryUsageMetrics(evt);
			};
			const recordMemoryPressure = (evt) => {
				const attrs = {
					"openclaw.memory.level": evt.level,
					"openclaw.memory.reason": evt.reason
				};
				memoryPressureCounter.add(1, attrs);
				recordMemoryUsageMetrics(evt, attrs);
				if (!tracesEnabled) return;
				const span = spanWithDuration("openclaw.memory.pressure", {
					...attrs,
					"openclaw.memory.rss_bytes": evt.memory.rssBytes,
					"openclaw.memory.heap_used_bytes": evt.memory.heapUsedBytes,
					"openclaw.memory.heap_total_bytes": evt.memory.heapTotalBytes,
					"openclaw.memory.external_bytes": evt.memory.externalBytes,
					"openclaw.memory.array_buffers_bytes": evt.memory.arrayBuffersBytes,
					...evt.thresholdBytes !== void 0 ? { "openclaw.memory.threshold_bytes": evt.thresholdBytes } : {},
					...evt.rssGrowthBytes !== void 0 ? { "openclaw.memory.rss_growth_bytes": evt.rssGrowthBytes } : {},
					...evt.windowMs !== void 0 ? { "openclaw.memory.window_ms": evt.windowMs } : {}
				}, 0, { endTimeMs: evt.ts });
				if (evt.level === "critical") span.setStatus({
					code: SpanStatusCode.ERROR,
					message: evt.reason
				});
				span.end(evt.ts);
			};
			const recordRunCompleted = (evt, metadata) => {
				const attrs = {
					"openclaw.outcome": evt.outcome,
					"openclaw.provider": evt.provider ?? "unknown",
					"openclaw.model": evt.model ?? "unknown"
				};
				if (evt.channel) attrs["openclaw.channel"] = evt.channel;
				durationHistogram.record(evt.durationMs, attrs);
				if (!tracesEnabled) return;
				const spanAttrs = { "openclaw.outcome": evt.outcome };
				addRunAttrs(spanAttrs, evt);
				if (evt.errorCategory) spanAttrs["openclaw.errorCategory"] = lowCardinalityAttr(evt.errorCategory, "other");
				const span = spanWithDuration("openclaw.run", spanAttrs, evt.durationMs, {
					parentContext: contextForTrustedDiagnosticSpanParent(evt, metadata),
					endTimeMs: evt.ts
				});
				if (evt.outcome === "error") span.setStatus({
					code: SpanStatusCode.ERROR,
					...evt.errorCategory ? { message: redactSensitiveText(evt.errorCategory) } : {}
				});
				span.end(evt.ts);
			};
			const harnessRunMetricAttrs = (evt) => ({
				"openclaw.harness.id": lowCardinalityAttr(evt.harnessId, "unknown"),
				"openclaw.harness.plugin": lowCardinalityAttr(evt.pluginId),
				"openclaw.outcome": evt.type === "harness.run.error" ? "error" : evt.outcome,
				"openclaw.provider": lowCardinalityAttr(evt.provider, "unknown"),
				"openclaw.model": lowCardinalityAttr(evt.model, "unknown"),
				...evt.channel ? { "openclaw.channel": lowCardinalityAttr(evt.channel) } : {}
			});
			const recordHarnessRunCompleted = (evt, metadata) => {
				harnessDurationHistogram.record(evt.durationMs, harnessRunMetricAttrs(evt));
				if (!tracesEnabled) return;
				const spanAttrs = { ...harnessRunMetricAttrs(evt) };
				if (evt.resultClassification) spanAttrs["openclaw.harness.result_classification"] = lowCardinalityAttr(evt.resultClassification);
				if (typeof evt.yieldDetected === "boolean") spanAttrs["openclaw.harness.yield_detected"] = evt.yieldDetected;
				if (evt.itemLifecycle) {
					spanAttrs["openclaw.harness.items.started"] = evt.itemLifecycle.startedCount;
					spanAttrs["openclaw.harness.items.completed"] = evt.itemLifecycle.completedCount;
					spanAttrs["openclaw.harness.items.active"] = evt.itemLifecycle.activeCount;
				}
				const span = spanWithDuration("openclaw.harness.run", spanAttrs, evt.durationMs, {
					parentContext: contextForTrustedDiagnosticSpanParent(evt, metadata),
					endTimeMs: evt.ts
				});
				if (evt.outcome === "error") span.setStatus({
					code: SpanStatusCode.ERROR,
					message: "error"
				});
				span.end(evt.ts);
			};
			const recordHarnessRunError = (evt, metadata) => {
				const errorType = lowCardinalityAttr(evt.errorCategory, "other");
				const attrs = {
					...harnessRunMetricAttrs(evt),
					"openclaw.harness.phase": evt.phase,
					"openclaw.errorCategory": errorType
				};
				harnessDurationHistogram.record(evt.durationMs, attrs);
				if (!tracesEnabled) return;
				const span = spanWithDuration("openclaw.harness.run", {
					...attrs,
					"error.type": errorType,
					...evt.cleanupFailed ? { "openclaw.harness.cleanup_failed": true } : {}
				}, evt.durationMs, {
					parentContext: contextForTrustedDiagnosticSpanParent(evt, metadata),
					endTimeMs: evt.ts
				});
				span.setStatus({
					code: SpanStatusCode.ERROR,
					message: errorType
				});
				span.end(evt.ts);
			};
			const recordContextAssembled = (evt, metadata) => {
				if (!tracesEnabled) return;
				const spanAttrs = {
					"openclaw.context.message_count": evt.messageCount,
					"openclaw.context.history_text_chars": evt.historyTextChars,
					"openclaw.context.history_image_blocks": evt.historyImageBlocks,
					"openclaw.context.max_message_text_chars": evt.maxMessageTextChars,
					"openclaw.context.system_prompt_chars": evt.systemPromptChars,
					"openclaw.context.prompt_chars": evt.promptChars,
					"openclaw.context.prompt_images": evt.promptImages
				};
				addRunAttrs(spanAttrs, evt);
				if (evt.contextTokenBudget !== void 0) spanAttrs["openclaw.context.token_budget"] = evt.contextTokenBudget;
				if (evt.reserveTokens !== void 0) spanAttrs["openclaw.context.reserve_tokens"] = evt.reserveTokens;
				spanWithDuration("openclaw.context.assembled", spanAttrs, 0, {
					parentContext: contextForTrustedDiagnosticSpanParent(evt, metadata),
					endTimeMs: evt.ts
				}).end(evt.ts);
			};
			const modelCallMetricAttrs = (evt) => ({
				"openclaw.provider": evt.provider,
				"openclaw.model": evt.model,
				"openclaw.api": lowCardinalityAttr(evt.api),
				"openclaw.transport": lowCardinalityAttr(evt.transport)
			});
			const genAiModelCallMetricAttrs = (evt, errorType) => ({
				"gen_ai.operation.name": genAiOperationName(evt.api),
				"gen_ai.provider.name": lowCardinalityAttr(evt.provider),
				"gen_ai.request.model": lowCardinalityAttr(evt.model),
				...errorType ? { "error.type": errorType } : {}
			});
			const recordModelCallCompleted = (evt, metadata) => {
				modelCallDurationHistogram.record(evt.durationMs, modelCallMetricAttrs(evt));
				genAiOperationDurationHistogram.record(evt.durationMs / 1e3, genAiModelCallMetricAttrs(evt));
				if (!tracesEnabled) return;
				const spanAttrs = {
					"openclaw.provider": evt.provider,
					"openclaw.model": evt.model
				};
				assignGenAiModelCallAttrs(spanAttrs, evt);
				if (evt.api) spanAttrs["openclaw.api"] = evt.api;
				if (evt.transport) spanAttrs["openclaw.transport"] = evt.transport;
				assignOtelModelContentAttributes(spanAttrs, evt, contentCapturePolicy);
				const span = spanWithDuration("openclaw.model.call", spanAttrs, evt.durationMs, {
					parentContext: contextForTrustedDiagnosticSpanParent(evt, metadata),
					endTimeMs: evt.ts
				});
				addUpstreamRequestIdSpanEvent(span, evt.upstreamRequestIdHash);
				span.end(evt.ts);
			};
			const recordModelCallError = (evt, metadata) => {
				const errorType = lowCardinalityAttr(evt.errorCategory, "other");
				modelCallDurationHistogram.record(evt.durationMs, {
					...modelCallMetricAttrs(evt),
					"openclaw.errorCategory": errorType
				});
				genAiOperationDurationHistogram.record(evt.durationMs / 1e3, genAiModelCallMetricAttrs(evt, errorType));
				if (!tracesEnabled) return;
				const spanAttrs = {
					"openclaw.provider": evt.provider,
					"openclaw.model": evt.model,
					"openclaw.errorCategory": errorType,
					"error.type": errorType
				};
				assignGenAiModelCallAttrs(spanAttrs, evt);
				if (evt.api) spanAttrs["openclaw.api"] = evt.api;
				if (evt.transport) spanAttrs["openclaw.transport"] = evt.transport;
				assignOtelModelContentAttributes(spanAttrs, evt, contentCapturePolicy);
				const span = spanWithDuration("openclaw.model.call", spanAttrs, evt.durationMs, {
					parentContext: contextForTrustedDiagnosticSpanParent(evt, metadata),
					endTimeMs: evt.ts
				});
				addUpstreamRequestIdSpanEvent(span, evt.upstreamRequestIdHash);
				span.setStatus({
					code: SpanStatusCode.ERROR,
					message: redactSensitiveText(evt.errorCategory)
				});
				span.end(evt.ts);
			};
			const recordToolExecutionCompleted = (evt, metadata) => {
				const attrs = {
					"openclaw.toolName": evt.toolName,
					...paramsSummaryAttrs(evt.paramsSummary)
				};
				toolExecutionDurationHistogram.record(evt.durationMs, attrs);
				if (!tracesEnabled) return;
				const spanAttrs = {
					"openclaw.toolName": evt.toolName,
					"gen_ai.tool.name": evt.toolName,
					...paramsSummaryAttrs(evt.paramsSummary)
				};
				addRunAttrs(spanAttrs, evt);
				assignOtelToolContentAttributes(spanAttrs, evt, contentCapturePolicy);
				spanWithDuration("openclaw.tool.execution", spanAttrs, evt.durationMs, {
					parentContext: contextForTrustedDiagnosticSpanParent(evt, metadata),
					endTimeMs: evt.ts
				}).end(evt.ts);
			};
			const recordToolExecutionError = (evt, metadata) => {
				const attrs = {
					"openclaw.toolName": evt.toolName,
					"openclaw.errorCategory": lowCardinalityAttr(evt.errorCategory, "other"),
					...paramsSummaryAttrs(evt.paramsSummary)
				};
				toolExecutionDurationHistogram.record(evt.durationMs, attrs);
				if (!tracesEnabled) return;
				const spanAttrs = {
					"openclaw.toolName": evt.toolName,
					"openclaw.errorCategory": lowCardinalityAttr(evt.errorCategory, "other"),
					"gen_ai.tool.name": evt.toolName,
					...paramsSummaryAttrs(evt.paramsSummary)
				};
				addRunAttrs(spanAttrs, evt);
				if (evt.errorCode) spanAttrs["openclaw.errorCode"] = lowCardinalityAttr(evt.errorCode, "other");
				assignOtelToolContentAttributes(spanAttrs, evt, contentCapturePolicy);
				const span = spanWithDuration("openclaw.tool.execution", spanAttrs, evt.durationMs, {
					parentContext: contextForTrustedDiagnosticSpanParent(evt, metadata),
					endTimeMs: evt.ts
				});
				span.setStatus({
					code: SpanStatusCode.ERROR,
					message: redactSensitiveText(evt.errorCategory)
				});
				span.end(evt.ts);
			};
			const recordExecProcessCompleted = (evt) => {
				const attrs = {
					"openclaw.exec.target": evt.target,
					"openclaw.exec.mode": evt.mode,
					"openclaw.outcome": evt.outcome
				};
				if (evt.failureKind) attrs["openclaw.failureKind"] = evt.failureKind;
				execProcessDurationHistogram.record(evt.durationMs, attrs);
				if (!tracesEnabled) return;
				const spanAttrs = {
					...attrs,
					"openclaw.exec.command_length": evt.commandLength
				};
				if (typeof evt.exitCode === "number") spanAttrs["openclaw.exec.exit_code"] = evt.exitCode;
				if (evt.exitSignal) spanAttrs["openclaw.exec.exit_signal"] = lowCardinalityAttr(evt.exitSignal, "other");
				if (evt.timedOut !== void 0) spanAttrs["openclaw.exec.timed_out"] = evt.timedOut;
				const span = spanWithDuration("openclaw.exec", spanAttrs, evt.durationMs, { endTimeMs: evt.ts });
				if (evt.outcome === "failed") span.setStatus({
					code: SpanStatusCode.ERROR,
					...evt.failureKind ? { message: evt.failureKind } : {}
				});
				span.end(evt.ts);
			};
			const recordHeartbeat = (evt) => {
				queueDepthHistogram.record(evt.queued, { "openclaw.channel": "heartbeat" });
			};
			const recordTelemetryExporter = (evt, metadata) => {
				if (!metadata.trusted) return;
				telemetryExporterCounter.add(1, {
					"openclaw.exporter": lowCardinalityAttr(evt.exporter, "unknown"),
					"openclaw.signal": evt.signal,
					"openclaw.status": evt.status,
					...evt.reason ? { "openclaw.reason": evt.reason } : {},
					...evt.errorCategory ? { "openclaw.errorCategory": lowCardinalityAttr(evt.errorCategory, "other") } : {}
				});
			};
			const subscribe = ctx.internalDiagnostics?.onEvent;
			if (!subscribe) {
				ctx.logger.error("diagnostics-otel: internal diagnostics capability unavailable");
				return;
			}
			unsubscribe = subscribe((evt, metadata) => {
				try {
					switch (evt.type) {
						case "model.usage":
							recordModelUsage(evt, metadata);
							return;
						case "webhook.received":
							recordWebhookReceived(evt);
							return;
						case "webhook.processed":
							recordWebhookProcessed(evt);
							return;
						case "webhook.error":
							recordWebhookError(evt);
							return;
						case "message.queued":
							recordMessageQueued(evt);
							return;
						case "message.processed":
							recordMessageProcessed(evt);
							return;
						case "message.delivery.started":
							recordMessageDeliveryStarted(evt);
							return;
						case "message.delivery.completed":
							recordMessageDeliveryCompleted(evt);
							return;
						case "message.delivery.error":
							recordMessageDeliveryError(evt);
							return;
						case "queue.lane.enqueue":
							recordLaneEnqueue(evt);
							return;
						case "queue.lane.dequeue":
							recordLaneDequeue(evt);
							return;
						case "session.state":
							recordSessionState(evt);
							return;
						case "session.stuck":
							recordSessionStuck(evt);
							return;
						case "run.attempt":
							recordRunAttempt(evt);
							return;
						case "diagnostic.heartbeat":
							recordHeartbeat(evt);
							return;
						case "run.completed":
							recordRunCompleted(evt, metadata);
							return;
						case "harness.run.completed":
							recordHarnessRunCompleted(evt, metadata);
							return;
						case "harness.run.error":
							recordHarnessRunError(evt, metadata);
							return;
						case "context.assembled":
							recordContextAssembled(evt, metadata);
							return;
						case "model.call.completed":
							recordModelCallCompleted(evt, metadata);
							return;
						case "model.call.error":
							recordModelCallError(evt, metadata);
							return;
						case "tool.execution.completed":
							recordToolExecutionCompleted(evt, metadata);
							return;
						case "tool.execution.error":
							recordToolExecutionError(evt, metadata);
							return;
						case "exec.process.completed":
							recordExecProcessCompleted(evt);
							return;
						case "log.record":
							recordLogRecord?.(evt, metadata);
							return;
						case "tool.loop":
							recordToolLoop(evt);
							return;
						case "diagnostic.memory.sample":
							recordMemorySample(evt);
							return;
						case "diagnostic.memory.pressure":
							recordMemoryPressure(evt);
							return;
						case "telemetry.exporter":
							recordTelemetryExporter(evt, metadata);
							return;
						case "tool.execution.started":
						case "run.started":
						case "harness.run.started":
						case "model.call.started":
						case "payload.large": return;
					}
				} catch (err) {
					ctx.logger.error(`diagnostics-otel: event handler failed (${evt.type}): ${formatError(err)}`);
				}
			});
			emitForSignals(enabledSignals, {
				exporter: "diagnostics-otel",
				status: "started",
				reason: "configured"
			});
			if (logsEnabled) ctx.logger.info("diagnostics-otel: logs exporter enabled (OTLP/Protobuf)");
		},
		async stop() {
			await stopStarted();
		}
	};
}
//#endregion
//#region extensions/diagnostics-otel/index.ts
var diagnostics_otel_default = definePluginEntry({
	id: "diagnostics-otel",
	name: "Diagnostics OpenTelemetry",
	description: "Export diagnostics events to OpenTelemetry",
	register(api) {
		api.registerService(createDiagnosticsOtelService());
	}
});
//#endregion
export { diagnostics_otel_default as default };
