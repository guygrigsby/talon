#!/usr/bin/env node
//
// openclaw-plugin-host: Node.js subprocess that loads one openclaw
// extension and bridges it to talon's gRPC Plugin service.
//
// USAGE
//   openclaw-plugin-host <extension-path>
//
// Where <extension-path> is either an extension directory (must contain
// package.json with `openclaw.extensions` or an `index.js`) or a path
// directly to the extension's entry .js / .mjs file.
//
// The shim refuses to start without the talon handshake env vars, so
// invoking it standalone produces a clear error.

import { requireHandshakeEnv, emitHandshake } from "../src/handshake.mjs";
import { resolveExtensionEntry, loadExtension } from "../src/loader.mjs";
import { startServer } from "../src/grpc-server.mjs";

async function main() {
  // Handshake validation comes first — accidental standalone runs die
  // before we touch the filesystem or load any extension code.
  let handshake;
  try {
    handshake = requireHandshakeEnv();
  } catch (err) {
    process.stderr.write(`openclaw-plugin-host: ${err.message}\n`);
    process.exit(1);
  }

  const extPathArg = process.argv[2];
  if (!extPathArg) {
    process.stderr.write(
      "openclaw-plugin-host: missing extension path argv (usage: openclaw-plugin-host <path>)\n",
    );
    process.exit(1);
  }

  let entryAbs;
  try {
    entryAbs = await resolveExtensionEntry(extPathArg);
  } catch (err) {
    process.stderr.write(`openclaw-plugin-host: resolve extension: ${err.message}\n`);
    process.exit(1);
  }

  let loaded;
  try {
    loaded = await loadExtension(entryAbs);
  } catch (err) {
    process.stderr.write(`openclaw-plugin-host: load extension ${entryAbs}: ${err?.stack ?? err}\n`);
    process.exit(1);
  }

  const { server, address } = await startServer(loaded);

  // Print the handshake AFTER the gRPC listener is bound. The host
  // reads stdout and starts dialing the address as soon as it sees
  // this line; emitting earlier would race.
  emitHandshake(address);

  // Hand off to gRPC for the rest of the lifetime. The Shutdown
  // handler in grpc-server triggers process exit; we only get here
  // again on uncaught errors.
  process.on("SIGTERM", () => {
    server.tryShutdown(() => process.exit(0));
  });
  process.on("SIGINT", () => {
    server.tryShutdown(() => process.exit(0));
  });

  // Suppress the "ref" hold so process.exit from the Shutdown handler
  // wins over event-loop activity.
  // (No explicit await — server.bindAsync already started serving.)
  // Cookie+hostAddr are stashed on the env for future Phase-2 use
  // (Host-side callbacks); reference them so the linter doesn't trim.
  void handshake;
}

main().catch((err) => {
  process.stderr.write(`openclaw-plugin-host: fatal: ${err?.stack ?? err}\n`);
  process.exit(1);
});
