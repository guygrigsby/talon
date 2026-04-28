import { pathToFileURL } from "node:url";
import path from "node:path";
import fs from "node:fs/promises";

import { Captured, buildApi } from "./api-shim.mjs";

/**
 * Resolve an openclaw extension entry point from a path that may be:
 *   - a directory containing package.json with `openclaw.extensions`
 *   - a directory containing index.js
 *   - a file (used directly)
 *
 * Returns an absolute path to the entry .js / .mjs file. Symmetric to
 * how openclaw's own loader resolves bundled extensions.
 */
export async function resolveExtensionEntry(rawPath) {
  const abs = path.resolve(rawPath);
  const stat = await fs.stat(abs).catch(() => null);
  if (!stat) {
    throw new Error(`extension path does not exist: ${abs}`);
  }
  if (stat.isFile()) {
    return abs;
  }
  if (stat.isDirectory()) {
    // Try package.json's openclaw.extensions first.
    const pkgPath = path.join(abs, "package.json");
    try {
      const pkg = JSON.parse(await fs.readFile(pkgPath, "utf8"));
      const entry = pkg?.openclaw?.extensions?.[0];
      if (typeof entry === "string" && entry.length > 0) {
        return path.resolve(abs, entry);
      }
    } catch {
      // fall through to index.js
    }
    // Fall back to index.{mjs,js}.
    for (const candidate of ["index.mjs", "index.js"]) {
      const p = path.join(abs, candidate);
      if (
        await fs
          .stat(p)
          .then((s) => s.isFile())
          .catch(() => false)
      ) {
        return p;
      }
    }
  }
  throw new Error(`could not find extension entry under ${abs}`);
}

/**
 * Load an extension via dynamic import and call its `register(api)`.
 * Returns the populated Captured + the resolved DefinedPluginEntry so
 * the gRPC server can return its id/description in the manifest.
 *
 * Extensions exported via `definePluginEntry({ register })` use a
 * `default` ESM export — that's what we look for first. Plain modules
 * that export a `register` function directly are also accepted.
 */
export async function loadExtension(entryAbs) {
  const url = pathToFileURL(entryAbs).href;
  const mod = await import(url);
  const def = mod?.default ?? mod;
  const register = typeof def === "function" ? def : def?.register;
  if (typeof register !== "function") {
    throw new Error(
      `extension at ${entryAbs} did not export a register function (default-export should be the result of definePluginEntry)`,
    );
  }
  const captured = new Captured();
  const api = buildApi(captured);
  await Promise.resolve(register(api));
  return {
    captured,
    id: typeof def?.id === "string" ? def.id : "",
    name: typeof def?.name === "string" ? def.name : "",
    description: typeof def?.description === "string" ? def.description : "",
  };
}
