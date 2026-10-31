// Code highlighting for the chat: Shiki with its JavaScript regex engine (no
// WebAssembly under the app's content policy), GitHub's light and dark
// themes side by side so the system appearance picks, and grammars loaded
// the first time a language shows up.

import { createHighlighterCore, type HighlighterCore } from "shiki/core";
import { createJavaScriptRegexEngine } from "shiki/engine/javascript";
import { bundledLanguages } from "shiki/langs";

const aliases: Record<string, string> = {
  ts: "typescript",
  tsx: "tsx",
  js: "javascript",
  jsx: "jsx",
  sh: "bash",
  shell: "bash",
  zsh: "bash",
  console: "bash",
  py: "python",
  rb: "ruby",
  rs: "rust",
  yml: "yaml",
  md: "markdown",
  golang: "go",
  "c++": "cpp",
  "c#": "csharp",
  cs: "csharp",
  kt: "kotlin",
  dockerfile: "docker",
  tf: "hcl",
  diff: "diff",
  patch: "diff",
};

let core: Promise<HighlighterCore> | null = null;
const loading = new Map<string, Promise<boolean>>();

function highlighter(): Promise<HighlighterCore> {
  core ??= createHighlighterCore({
    themes: [import("shiki/themes/github-light-default.mjs"), import("shiki/themes/github-dark-default.mjs")],
    langs: [],
    engine: createJavaScriptRegexEngine({ forgiving: true }),
  });
  return core;
}

/** The grammar name for a fence's language, or null when there is none. */
export function grammarOf(lang: string | undefined): string | null {
  if (!lang) return null;
  const l = lang.toLowerCase();
  const name = aliases[l] ?? l;
  return name in bundledLanguages ? name : null;
}

async function ensure(h: HighlighterCore, lang: string): Promise<boolean> {
  if (h.getLoadedLanguages().includes(lang)) return true;
  let p = loading.get(lang);
  if (!p) {
    const load = bundledLanguages[lang as keyof typeof bundledLanguages];
    p = load
      ? load()
          .then((m) => h.loadLanguage(m.default))
          .then(() => true)
          .catch(() => false)
      : Promise.resolve(false);
    loading.set(lang, p);
  }
  return p;
}

const cache = new Map<string, string>();

/** Highlighted HTML for code, or null when the language is unknown. */
export async function highlight(code: string, lang: string | undefined): Promise<string | null> {
  const grammar = grammarOf(lang);
  if (!grammar || code.length > 200_000) return null;
  const key = `${grammar}\u0000${code}`;
  const hit = cache.get(key);
  if (hit) return hit;
  const h = await highlighter();
  if (!(await ensure(h, grammar))) return null;
  const html = h.codeToHtml(code, {
    lang: grammar,
    themes: { light: "github-light-default", dark: "github-dark-default" },
    defaultColor: false,
  });
  if (cache.size > 400) cache.clear();
  cache.set(key, html);
  return html;
}
