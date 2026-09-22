import type { ComponentType } from "react";
import { transform } from "sucrase";
import * as React from "react";
import * as JsxRuntime from "react/jsx-runtime";

// Live renderer for project-generated component twins (the human-mockups
// skill's 3b form): fetches a manifest-listed tsx from the daemon's
// /api/mockup-sets/{slug}/source/{file}, transforms it in the browser, and
// executes it with an import surface limited to react and the sandbox's own
// UI kit — exactly the imports the skill's contract allows. The embedded
// desktop bundle is built ahead of time, so project components can never join
// the build-time module map; this is the dynamic counterpart of
// .generated/mockup-components.

type Loader = () => Promise<Record<string, unknown>>;

// Every UI-kit component a twin may import, statically discovered and
// code-split at build time via the glob. Preloaded once before the first
// dynamic render so the SYNCHRONOUS require() inside transformed code can
// resolve them from a map.
const kitLoaders = import.meta.glob("/src/components/ui/*.tsx") as Record<
  string,
  Loader
>;

const kitModules = new Map<string, Record<string, unknown>>();
let kitPreload: Promise<void> | null = null;

function preloadKit(): Promise<void> {
  kitPreload ??= Promise.all(
    Object.entries(kitLoaders).map(async ([kitPath, load]) => {
      const name = kitPath.slice(
        kitPath.lastIndexOf("/") + 1,
        -".tsx".length,
      );
      kitModules.set(name, (await load()) as Record<string, unknown>);
    }),
  ).then(() => undefined);
  return kitPreload;
}

function requireModule(name: string): unknown {
  if (name === "react") return React;
  if (name === "react/jsx-runtime") return JsxRuntime;
  const kit = /^@\/components\/ui\/([a-z0-9-]+)$/.exec(name);
  if (kit) {
    const mod = kitModules.get(kit[1]);
    if (mod) return mod;
    throw new Error(`UI kit module "${name}" is not loaded yet`);
  }
  throw new Error(
    `Import "${name}" is outside the mockup component sandbox (react and @/components/ui/* only)`,
  );
}

const cache = new Map<string, Promise<ComponentType>>();

// loadDynamicMockup resolves <slug>'s component twin <file> to a renderable
// component. Failures are cached like successes: a twin that 404s or throws
// stays failed until reload — retrying on every render would re-fetch and
// re-throw in a loop under React's renderer.
export function loadDynamicMockup(
  slug: string,
  file: string,
): Promise<ComponentType> {
  const key = `${slug}/${file}`;
  let entry = cache.get(key);
  if (!entry) {
    entry = preloadKit().then(async () => {
      const res = await fetch(
        `/api/mockup-sets/${encodeURIComponent(slug)}/source/${encodeURIComponent(file)}`,
      );
      if (!res.ok) {
        const body = (await res.json().catch(() => null)) as {
          error?: string;
        } | null;
        throw new Error(
          `The daemon returned ${res.status}: ${body?.error ?? res.statusText}`,
        );
      }
      const source = await res.text();
      // "imports" converts the twin's ESM import statements to require()
      // calls against the limited surface below — plain Function scope has
      // no module loader of its own.
      const compiled = transform(source, {
        transforms: ["typescript", "jsx", "imports"],
        jsxRuntime: "automatic",
        production: true,
      }).code;
      const exports: Record<string, unknown> = {};
      new Function("require", "exports", compiled)(requireModule, exports);
      const component =
        (exports.default as ComponentType | undefined) ??
        (Object.values(exports).find((v) => typeof v === "function") as
          | ComponentType
          | undefined);
      if (!component) {
        throw new Error("The component file has no exported function component");
      }
      return component;
    });
    cache.set(key, entry);
  }
  return entry;
}
