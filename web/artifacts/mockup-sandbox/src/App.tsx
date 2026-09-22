import { useEffect, useState, type ComponentType } from "react";

import { useListMockupSets } from "@workspace/api-client-react";

import { loadDynamicMockup } from "./lib/dynamicMockup";
import { modules as discoveredModules } from "./.generated/mockup-components";

type ModuleMap = Record<string, () => Promise<Record<string, unknown>>>;

function _resolveComponent(
  mod: Record<string, unknown>,
  name: string,
): ComponentType | undefined {
  const fns = Object.values(mod).filter(
    (v) => typeof v === "function",
  ) as ComponentType[];
  return (
    (mod.default as ComponentType) ||
    (mod.Preview as ComponentType) ||
    (mod[name] as ComponentType) ||
    fns[fns.length - 1]
  );
}

function PreviewRenderer({
  componentPath,
  modules,
}: {
  componentPath: string;
  modules: ModuleMap;
}) {
  const [Component, setComponent] = useState<ComponentType | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;

    setComponent(null);
    setError(null);

    async function loadComponent(): Promise<void> {
      const key = `./components/mockups/${componentPath}.tsx`;
      const loader = modules[key];
      if (!loader) {
        setError(
          `No component at ${componentPath}.tsx — generate it with the /human-mockups skill in the project.`,
        );
        return;
      }

      try {
        const mod = await loader();
        if (cancelled) {
          return;
        }
        const name = componentPath.split("/").pop()!;
        const comp = _resolveComponent(mod, name);
        if (!comp) {
          setError(
            `No exported React component found in ${componentPath}.tsx\n\nMake sure the file has at least one exported function component.`,
          );
          return;
        }
        setComponent(() => comp);
      } catch (e) {
        if (cancelled) {
          return;
        }

        const message = e instanceof Error ? e.message : String(e);
        setError(`Failed to load preview.\n${message}`);
      }
    }

    void loadComponent();

    return () => {
      cancelled = true;
    };
  }, [componentPath, modules]);

  if (error) {
    return <NotFound title="Preview not found" detail={error} />;
  }

  if (!Component) return null;

  return <Component />;
}

function getBasePath(): string {
  return import.meta.env.BASE_URL.replace(/\/$/, "");
}

// The /mocks/ base path is mounted only by the desktop app's embedded
// middleware (desktop/webdist.go); every other context — vite dev, a hosted
// preview — serves this app at "/". Copy aimed at a desktop user is wrong
// on the web, so the empty state branches on it.
function isDesktopEmbed(): boolean {
  return getBasePath() !== "";
}

// NotFound is the friendly miss screen for this app's own routes: a preview
// path with no matching component (the old screen was a raw red <pre>).
function NotFound({ title, detail }: { title: string; detail: string }) {
  return (
    <div className="min-h-screen bg-gray-50 flex items-center justify-center p-8">
      <div className="text-center max-w-md">
        <h1 className="text-xl font-semibold text-gray-900 mb-2">{title}</h1>
        <p className="text-sm text-gray-500 mb-4 break-words">{detail}</p>
        <a
          className="text-sm text-blue-600 hover:underline"
          href={getBasePath() || "/"}
        >
          Back to the gallery
        </a>
      </div>
    </div>
  );
}

function getPreviewExamplePath(): string {
  const basePath = getBasePath();
  return `${basePath}/preview/ComponentName`;
}

function Gallery() {
  // The daemon's /api surface is the source of truth (webapi.go): the same
  // manifests the desktop board lists, newest first.
  const { data: sets, isPending, isError, error } = useListMockupSets();

  if (isPending) {
    return (
      <div className="min-h-screen bg-gray-50 flex items-center justify-center p-8">
        <p className="text-gray-500">Scanning mockup sets…</p>
      </div>
    );
  }

  if (isError) {
    return (
      <div className="min-h-screen bg-gray-50 flex items-center justify-center p-8">
        <div className="text-center max-w-md">
          <h1 className="text-xl font-semibold text-gray-900 mb-2">
            Cannot reach the human daemon
          </h1>
          <p className="text-gray-500 mb-2">
            This page reads mockup manifests over the daemon's /api surface on
            127.0.0.1:19285. Start it (or the desktop app, which runs one)
            and reload.
          </p>
          <p className="text-xs text-gray-400 font-mono">
            {error instanceof Error ? error.message : String(error)}
          </p>
        </div>
      </div>
    );
  }

  if (!sets || sets.length === 0) {
    return (
      <div className="min-h-screen bg-gray-50 flex items-center justify-center p-8">
        <div className="text-center max-w-md">
          <h1 className="text-xl font-semibold text-gray-900 mb-2">
            No mockup sets yet
          </h1>
          <p className="text-gray-500 mb-4">
            The daemon's projects hold no mockups/&lt;slug&gt;/index.json. Run
            the /human-mockups skill in a project and the set appears here.
          </p>
          <p className="text-sm text-gray-400">
            {isDesktopEmbed()
              ? "Open the Mockups view in the desktop app once a set exists."
              : "Component previews render at " + getPreviewExamplePath()}
          </p>
        </div>
      </div>
    );
  }

  return (
    <div className="min-h-screen bg-gray-50 p-8">
      <h1 className="text-2xl font-semibold text-gray-900 mb-1">Mockup sets</h1>
      <p className="text-sm text-gray-500 mb-6">
        Served from the daemon's projects · newest first
      </p>
      <ul className="space-y-3 max-w-2xl">
        {sets.map((set) => (
          <li
            key={set.slug}
            className="bg-white border border-gray-200 rounded-lg p-4"
          >
            <div className="flex items-baseline justify-between gap-4">
              <span className="font-medium text-gray-900">
                {set.feature || set.slug}
              </span>
              <span className="text-xs text-gray-400">{set.project}</span>
            </div>
            <p className="text-sm text-gray-500 mt-1">
              {set.options.length} option{set.options.length === 1 ? "" : "s"} ·{" "}
              <code className="text-xs">{set.slug}</code>
            </p>
            <ul className="mt-2 flex flex-wrap gap-2">
              {set.options.map((opt) => (
                <li key={opt.file}>
                  <a
                    className="text-sm text-blue-600 hover:underline"
                    href={`${getBasePath()}/mockups/${encodeURIComponent(set.slug)}/${encodeURIComponent(opt.file)}`}
                  >
                    {opt.name}
                  </a>
                  {opt.component && (
                    <a
                      className="text-xs text-gray-500 hover:text-blue-600 hover:underline ml-1.5"
                      href={`${getBasePath()}/preview/${encodeURIComponent(set.slug)}/${encodeURIComponent(opt.component)}`}
                      title="Live render of the component twin"
                    >
                      ⚛ live
                    </a>
                  )}
                </li>
              ))}
            </ul>
          </li>
        ))}
      </ul>
    </div>
  );
}

// EmbedFrame renders an iframe of <target> resolved against this app's base
// path — the combine plan's 3a bridge: the board's Mockups view opens
// /mocks/embed/mockups/<slug>/<file>, which lands here and frames the static
// set that the Go middleware serves at /mockups/<slug>/<file>. Query strings
// pass through so future params survive the hop.
function EmbedFrame({ target }: { target: string }) {
  const src = `${getBasePath()}/${target}`;
  return <iframe src={src} title="Mockup" className="h-screen w-screen border-0" />;
}

// getParsedPreviewPath splits this app's /preview/ routes into their two
// forms: {kind: "local"} for a component bundled into the sandbox itself
// (build-time discovery), {kind: "twin", slug} for a project-generated
// component twin served from disk via the daemon API (3b form:
// /preview/<slug>/<file.tsx>). The twin form is recognized by a second
// segment ending in .tsx.
type ParsedPreview =
  | { kind: "local"; componentPath: string }
  | { kind: "twin"; slug: string; file: string };

function getParsedPreviewPath(): ParsedPreview | null {
  const basePath = getBasePath();
  const { pathname } = window.location;
  const local =
    basePath && pathname.startsWith(basePath)
      ? pathname.slice(basePath.length) || "/"
      : pathname;
  const match = local.match(/^\/preview\/(.+)$/);
  if (!match) return null;
  const rest = match[1];
  const twin = rest.match(/^([^/]+)\/([^/]+\.tsx)$/);
  if (twin) {
    return { kind: "twin", slug: twin[1], file: twin[2] };
  }
  return { kind: "local", componentPath: rest };
}

// DynamicPreview loads a project-generated twin through the daemon and the
// runtime transform (see lib/dynamicMockup.tsx).
function DynamicPreview({ slug, file }: { slug: string; file: string }) {
  const [Component, setComponent] = useState<ComponentType | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    setComponent(null);
    setError(null);
    loadDynamicMockup(slug, file).then(
      (c) => {
        if (!cancelled) setComponent(() => c);
      },
      (e: unknown) => {
        if (!cancelled) {
          setError(
            e instanceof Error ? e.message : "Failed to load the component",
          );
        }
      },
    );
    return () => {
      cancelled = true;
    };
  }, [slug, file]);

  if (error) {
    return (
      <NotFound
        title="Component could not be rendered"
        detail={`${error} — the twin lives at mockups/${slug}/${file} in the project.`}
      />
    );
  }
  if (!Component) {
    return (
      <div className="min-h-screen bg-gray-50 flex items-center justify-center p-8">
        <p className="text-gray-500">Loading component…</p>
      </div>
    );
  }
  return <Component />;
}

// getEmbedTarget matches the board's deep-link bridge /embed/<target> and
// returns <target> plus the query string, or null. Like getPreviewPath it
// strips this app's base path first, so it works both at BASE_PATH=/mocks/
// (the desktop embed) and at the dev server root.
function getEmbedTarget(): string | null {
  const basePath = getBasePath();
  const { pathname, search } = window.location;
  const local =
    basePath && pathname.startsWith(basePath)
      ? pathname.slice(basePath.length) || "/"
      : pathname;
  const match = local.match(/^\/embed\/(.+)$/);
  return match ? match[1] + search : null;
}

function App() {
  const embedTarget = getEmbedTarget();
  if (embedTarget) {
    return <EmbedFrame target={embedTarget} />;
  }

  const preview = getParsedPreviewPath();

  if (preview) {
    if (preview.kind === "twin") {
      return (
        <DynamicPreview slug={preview.slug} file={preview.file} />
      );
    }
    return (
      <PreviewRenderer
        componentPath={preview.componentPath}
        modules={discoveredModules}
      />
    );
  }

  return <Gallery />;
}

export default App;
