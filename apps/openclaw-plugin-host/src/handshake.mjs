// Handshake constants must stay in sync with internal/plugin/handshake.go.
// The host expects exactly these env vars and the printed line format —
// any drift breaks the spawn handshake.

export const HANDSHAKE_VERSION = 1;
export const HANDSHAKE_MAGIC = "talon.plugin.v1";
export const ENV_HANDSHAKE = "TALON_PLUGIN_HANDSHAKE";
export const ENV_AUTH_COOKIE = "TALON_PLUGIN_AUTH_COOKIE";
export const ENV_HOST_ADDR = "TALON_PLUGIN_HOST_ADDR";
export const COOKIE_METADATA_KEY = "talon-plugin-auth-cookie";

/**
 * Validate the parent handshake env vars. Throws on failure (with a
 * stderr-friendly message) so an accidental standalone run dies loudly
 * instead of opening a port nobody dialed.
 */
export function requireHandshakeEnv() {
  const got = process.env[ENV_HANDSHAKE];
  if (got !== HANDSHAKE_MAGIC) {
    throw new Error(
      `${ENV_HANDSHAKE}=${JSON.stringify(got)}, want ${JSON.stringify(HANDSHAKE_MAGIC)} ` +
        "(refusing to start outside the host)",
    );
  }
  const cookie = process.env[ENV_AUTH_COOKIE] ?? "";
  if (cookie.length === 0) {
    throw new Error(`missing ${ENV_AUTH_COOKIE}`);
  }
  return {
    cookie,
    hostAddr: process.env[ENV_HOST_ADDR] ?? "",
  };
}

/**
 * Print the handshake line on stdout. Matches the Go ParseHandshakeLine
 * regex: "<version>|<network>|<address>|<protocol>".
 */
export function emitHandshake(address) {
  // Ensure a trailing newline; the host reads line-by-line.
  process.stdout.write(`${HANDSHAKE_VERSION}|TCP|${address}|grpc\n`);
}
