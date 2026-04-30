// api-shim provides a partial OpenClawPluginApi that an extension's
// register(api) method can call into. We capture every registration so
// the gRPC Initialize handler can build a Manifest from it, and
// RunTool/StartChannel/etc can dispatch to the captured handlers.
//
// PHASE 1 (MVP): only registerTool is fully wired. The rest of the API
// surface (registerHttpRoute, registerWebSearchProvider,
// registerCommand, ...) is captured-but-ignored: we record that the
// extension would have used it so users get a clear "shim doesn't yet
// support X" log line, but the lack of support doesn't crash the load.
//
// Adding a new register-method bridge later is mechanical: capture the
// call here, expose it on Captured, and translate in grpc-server.mjs.

/**
 * Captured is the bag the loader fills in via api.register*() calls.
 * Read by grpc-server when assembling Initialize responses.
 */
export class Captured {
  constructor() {
    /** @type {Array<{ name: string, factory: Function, options: object }>} */
    this.toolFactories = [];
    /** @type {Array<{ name: string, factory: Function, options: object }>} */
    this.providerFactories = [];
    /** @type {Array<{ name: string, factory: Function, options: object }>} */
    this.channelFactories = [];
    /** @type {Array<{ kind: string, args: any[] }>} */
    this.unsupported = [];
  }

  /**
   * Realize all register*() factories against a single context.
   * Tools/providers/channels share the same ctx so an extension can,
   * say, look up a provider's API key from ctx.config and reuse it
   * across tool factories.
   */
  realizeTools(ctx) {
    return realizeFactories(this.toolFactories, ctx, "tool");
  }
  realizeProviders(ctx) {
    return realizeFactories(this.providerFactories, ctx, "provider");
  }
  realizeChannels(ctx) {
    return realizeFactories(this.channelFactories, ctx, "channel");
  }
}

function realizeFactories(specs, ctx, kind) {
  /** @type {Map<string, any>} */
  const out = new Map();
  for (const { name: hint, factory, options } of specs) {
    let inst;
    try {
      inst = factory(ctx);
    } catch (err) {
      process.stderr.write(
        `[openclaw-shim] ${kind} factory for ${JSON.stringify(hint || options.name || "?")} threw: ${err?.stack ?? err}\n`,
      );
      continue;
    }
    const name = inst?.name ?? options?.name ?? hint;
    if (!name) {
      process.stderr.write(
        `[openclaw-shim] ${kind} factory returned an object without a name; dropping\n`,
      );
      continue;
    }
    out.set(name, inst);
  }
  return out;
}

/**
 * Build a partial OpenClawPluginApi for the extension's register(api)
 * call. Every method that mutates registry state writes into captured;
 * methods we don't yet bridge log a one-line warning so the user sees
 * what's missing.
 *
 * @param {Captured} captured
 */
export function buildApi(captured) {
  const warn = (kind, args) => {
    captured.unsupported.push({ kind, args });
    process.stderr.write(
      `[openclaw-shim] api.${kind}(): not yet bridged by the compat shim — ignoring\n`,
    );
  };

  return {
    // === bridged ====================================================
    registerTool(factory, options = {}) {
      capturePushFactory(captured.toolFactories, factory, options, "registerTool");
    },

    // === bridged in Phase 2 =========================================
    registerProvider(factory, options = {}) {
      capturePushFactory(captured.providerFactories, factory, options, "registerProvider");
    },
    registerChannel(factory, options = {}) {
      capturePushFactory(captured.channelFactories, factory, options, "registerChannel");
    },

    // === captured-but-ignored (later phases) ========================
    registerHttpRoute: (...args) => warn("registerHttpRoute", args),
    registerWebSearchProvider: (...args) => warn("registerWebSearchProvider", args),
    registerCommand: (...args) => warn("registerCommand", args),
    registerService: (...args) => warn("registerService", args),
    registerMemory: (...args) => warn("registerMemory", args),
    registerContextEngine: (...args) => warn("registerContextEngine", args),
    registerAgentHarness: (...args) => warn("registerAgentHarness", args),
    registerHook: (...args) => warn("registerHook", args),

    // Logger surface — extensions call api.logger.info/warn/error. Map
    // to stderr so the host captures it as `[plugin/<name>] <line>`.
    logger: {
      info: (...args) => process.stderr.write(`[shim:info] ${formatArgs(args)}\n`),
      warn: (...args) => process.stderr.write(`[shim:warn] ${formatArgs(args)}\n`),
      error: (...args) => process.stderr.write(`[shim:error] ${formatArgs(args)}\n`),
      debug: (...args) => process.stderr.write(`[shim:debug] ${formatArgs(args)}\n`),
    },
  };
}

// capturePushFactory is the shared push helper for registerTool /
// registerProvider / registerChannel. Accepts either a factory function
// or an already-built object (some extensions pass the latter for
// brevity); normalizes both to a factory(ctx) closure.
function capturePushFactory(target, factory, options, label) {
  if (typeof factory === "function") {
    target.push({ name: options?.name ?? "", factory, options: options ?? {} });
  } else if (factory && typeof factory === "object") {
    target.push({
      name: factory.name ?? options?.name ?? "",
      factory: () => factory,
      options: options ?? {},
    });
  } else {
    process.stderr.write(
      `[openclaw-shim] ${label}: ignored, expected function or object, got ${typeof factory}\n`,
    );
  }
}

function formatArgs(args) {
  return args
    .map((a) => {
      if (typeof a === "string") return a;
      try {
        return JSON.stringify(a);
      } catch {
        return String(a);
      }
    })
    .join(" ");
}
