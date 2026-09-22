---
name: human-mockups
description: Create annotated static HTML mockups exploring UI options for a feature, matched to the project's real look
argument-hint: <feature to explore> [number of options, default 5]
---

# UI Option Mockups

Produce N static HTML mockups (default 5), each showing a DIFFERENT interaction pattern for the requested feature, so the options can be compared side by side and one can be picked for implementation. A picked mockup can be iterated further via the variation invocation below and ultimately marked as the ticket's winner in the app, whereupon it is handed to planning/execution agents as the design direction. Each option is a pair: the annotated static HTML picture (`NN-short-name.html`) and a live React component twin (`NN-short-name.tsx`, see below) that the desktop sandbox renders from the project's real UI kit. The HTML files themselves carry no functionality and no JavaScript — they are pictures made of HTML.

All files for one invocation go into their own subdirectory `mockups/<feature-slug>/` (kebab-case, e.g. `mockups/permission-requests/`) so multiple explored features coexist. Never write mockup files into `mockups/` directly.

## Ticket-linked invocation

If the argument begins with an issue key followed by a colon (e.g. `<TICKET_KEY>: dark mode toggle`), the mockups belong to that ticket: use the lowercased key as the feature slug (`mockups/<ticket-key>/`), use the text after the colon as the feature name, and add a top-level `"ticket": "<TICKET_KEY>"` field to `index.json`. An optional `Ticket context:` block after the first line is background for choosing options — never render it in the mockups. Without a leading key, behave exactly as described below.

## Variation invocation

When invoked with `--variation <KEY>: <feature>` followed by a block:

```
Vary this existing mockup:
  group slug: <parentSlug>
  source file: mockups/<parentSlug>/<sourceFile>
Write the new group to: mockups/<childSlug>/
Change instructions:
<free text>
```

produce a NEW group that iterates on ONE existing mockup rather than exploring fresh alternatives:

- **Read the source file first** and treat it as the hard baseline. Every option in the new group is a *refinement of that one mockup* applying the change instructions — NOT fresh unrelated alternatives. Preserve the source's interaction paradigm, sample data, and app-shell context; vary only what the instructions ask for (plus tightly related follow-on choices).
- **Never modify or delete the source group.** Write ONLY into the given `mockups/<childSlug>/` directory. The parent must remain viewable unchanged.
- Default to **3 variations** (the exploration is narrower than a first round); still render distinct, comparable options.
- Write `mockups/<childSlug>/index.json` with the normal fields PLUS `"parent": "<parentSlug>"`, `"parentFile": "<sourceFile>"`, `"instructions": "<free text>"`, and `"ticket": "<KEY>"` (the same ticket as the parent). Keep the same brief-bar / annotation / verify-before-presenting rules as a first-round set.

## Ground rules

1. **Match the real app.** Locate the project's actual frontend (stylesheet, design tokens, app shell markup) and reproduce its look faithfully: colors, type, chrome, layout. Every mockup renders the pattern inside the real visual context, never on a blank page. If the project has no existing UI or several, ask which surface the mockups target before starting.
2. **Same data everywhere.** Invent one small, realistic sample dataset that fits the product (real-looking ticket keys, names, timestamps) and render the IDENTICAL data in every option. Options must differ only in the interaction pattern, never in content.
3. **Genuinely distinct options.** Each file is a different interaction paradigm (e.g. blocking modal / notification stack / dedicated panel / inline-in-context / persistent strip), not styling variants of one idea. If two drafts feel similar, replace one.
4. **Self-contained files.** Inline all CSS in each file. No external resources, no imports — each file must render standalone from disk.

## Anatomy of each mockup file (`NN-short-name.html`)

- **Brief bar** above the app frame (clearly outside it): an eyebrow label (`<Feature> · Option N of M`), the option name as a one-line thesis, a short concept paragraph, pros/cons chips (green `+` / red `−`, mono font), and prev/next/index links.
- **App frame**: bordered, rounded, drop-shadowed reproduction of the app shell at a fixed width (~1240px), with the option rendered in place.
- **Annotation notes**: high-contrast sticky notes (amber works well on dark UIs; numbered chips; `ui-monospace`) absolutely positioned next to the UI they explain. Each note states BEHAVIOR — what happens, when, and which API/backend call powers it — not visual description. Notes must sit on empty areas, never covering the UI they point at.
- **Footer line**: "Static mockup — no functionality" plus the real data source / API verbs the pattern would use.

## The component twin (`NN-short-name.tsx`)

Alongside each option's HTML, write a React component that renders the SAME interaction pattern with the project's real UI kit. This is what the desktop sandbox renders live at `/preview/<slug>/<file>` — the HTML is the annotated picture of the pattern, the tsx is the pattern itself:

- **Default-exported function component**, same NN stem as the HTML (`01-modal.html` ↔ `01-modal.tsx`). No props needed; hardcode the option's sample data inside.
- **Imports are limited to** `react`, and the project's UI kit via `@/components/ui/<name>` (e.g. `@/components/ui/button`, `@/components/ui/dialog`). NOTHING else: no app code, no fetches, no router, no CSS imports — the kit and inline Tailwind classes are the whole toolbox. The component must render inside a plain `div` without an app shell around it.
- **Interactive where the pattern is interactive**: a blocking modal should actually open/block, a notification stack should actually stack. Use local `useState` only; there is no backend — behavioral notes about real API calls stay in the HTML annotations.
- Keep the interaction paradigm identical to the HTML twin: same pattern, same sample data, same layout intent. A reviewer comparing HTML and preview must see the same option.

Also write, inside the feature subdirectory:

- `index.html`: linked cards for every option (name, one-liner, tag chips) and a closing hint on which options could combine.
- `index.json`: a machine-readable manifest so tools (the desktop sandbox, the daemon API) can list the set without parsing HTML:

```json
{
  "feature": "permission requests",
  "slug": "permission-requests",
  "created": "2026-07-11",
  "options": [
    {
      "n": 1,
      "name": "Blocking modal",
      "file": "01-modal.html",
      "component": "01-modal.tsx",
      "description": "Takeover dialog, one request at a time; nothing else clickable until decided."
    }
  ]
}
```

One entry per option, in order; `description` is the option's one-line thesis from its brief bar. `file` always names the HTML; `component` names the tsx twin and is present whenever the twin exists (it always should for new options). `ticket` is present only for ticket-linked invocations. Keep `index.json` in sync if options are added or revised — a component listed there is what makes the option render live in the app.

## Verify before presenting

Render every file headless and LOOK at it — absolutely-positioned notes overlap content on the first try more often than not:

1. Screenshot each HTML file with a headless browser, e.g. `chromium --headless --screenshot=NN.png --window-size=1400,1000 --hide-scrollbars file:///.../NN.html`. If only a sandboxed (Flatpak/Snap) browser is available, it may not read the project directory — copy the files to a directory it can access (e.g. `~/Downloads/tmp-mockcheck/`), render there, and delete the copy afterwards.
2. View each screenshot. Fix any note covering content, broken layout, or unreadable contrast; re-render until clean.
3. For each component twin: if the human daemon is running (or the desktop app is open), check the sandbox renders it — `http://localhost:5174/preview/<slug>/<NN-name.tsx>` in dev, or the Mockups view's component link in the app. A tsx that throws or renders blank is a broken option: fix the component (sticking to the import limits) until it renders. If no daemon is available, at least confirm the file compiles standalone (`pnpm --filter @workspace/mockup-sandbox exec tsc --noEmit` on a scratch import is NOT required — a careful read for the import rules is the minimum bar).

## Presenting

Summarize each option in one or two sentences with its sharpest pro and con, name the load-bearing differences, and offer a recommendation (including sensible combinations). Do NOT implement anything — the user picks first.

Note for the user: links between mockups only work in a browser that can see the whole directory. Sandboxed browsers opening a single file via the document portal will 404 on relative links — either grant the browser read access to `mockups/` or serve the directory with `python3 -m http.server`.
