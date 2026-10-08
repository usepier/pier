// The editor: CodeMirror with the app's fonts and colors, and a language
// picked by file extension (loaded on first use).
import { defaultKeymap, history, historyKeymap, indentWithTab } from "@codemirror/commands";
import {
  bracketMatching,
  foldGutter,
  HighlightStyle,
  indentOnInput,
  syntaxHighlighting,
} from "@codemirror/language";
import { highlightSelectionMatches, searchKeymap } from "@codemirror/search";
import type { Extension } from "@codemirror/state";
import {
  drawSelection,
  EditorView,
  highlightActiveLine,
  highlightActiveLineGutter,
  highlightSpecialChars,
  keymap,
  lineNumbers,
} from "@codemirror/view";
import { tags as t } from "@lezer/highlight";

const loaders: [RegExp, () => Promise<Extension>][] = [
  [
    /\.(m|c)?(j|t)sx?$/,
    async () => (await import("@codemirror/lang-javascript")).javascript({ typescript: true, jsx: true }),
  ],
  [/\.py$/, async () => (await import("@codemirror/lang-python")).python()],
  [/\.go$/, async () => (await import("@codemirror/lang-go")).go()],
  [/\.rs$/, async () => (await import("@codemirror/lang-rust")).rust()],
  [/\.(json|jsonc)$/, async () => (await import("@codemirror/lang-json")).json()],
  [/\.(css|scss|less)$/, async () => (await import("@codemirror/lang-css")).css()],
  [/\.(html?|vue|svelte)$/, async () => (await import("@codemirror/lang-html")).html()],
  [/\.(md|mdx|markdown)$/, async () => (await import("@codemirror/lang-markdown")).markdown()],
  [/\.(ya?ml)$/, async () => (await import("@codemirror/lang-yaml")).yaml()],
];

/** The language support for a path, if there is one. */
export async function languageFor(path: string): Promise<Extension> {
  const l = loaders.find(([re]) => re.test(path.toLowerCase()));
  return l ? l[1]() : [];
}

const highlight = HighlightStyle.define([
  { tag: [t.keyword, t.operatorKeyword, t.modifier, t.controlKeyword], color: "var(--hl-keyword)" },
  { tag: [t.string, t.special(t.string), t.regexp], color: "var(--hl-string)" },
  { tag: [t.number, t.bool, t.null, t.atom], color: "var(--hl-number)" },
  { tag: [t.comment, t.lineComment, t.blockComment], color: "var(--hl-comment)", fontStyle: "italic" },
  { tag: [t.function(t.variableName), t.function(t.propertyName)], color: "var(--hl-function)" },
  { tag: [t.typeName, t.className, t.namespace], color: "var(--hl-type)" },
  { tag: [t.propertyName, t.attributeName], color: "var(--hl-property)" },
  { tag: [t.tagName, t.heading], color: "var(--hl-tag)", fontWeight: "600" },
  { tag: [t.meta, t.processingInstruction], color: "var(--hl-comment)" },
  { tag: t.link, color: "var(--accent)", textDecoration: "underline" },
  { tag: t.invalid, color: "var(--color-bad)" },
]);

const theme = EditorView.theme({
  "&": { height: "100%", fontSize: "12.5px", backgroundColor: "var(--surface)", color: "var(--label)" },
  ".cm-scroller": { fontFamily: "var(--font-mono)", lineHeight: "1.5" },
  ".cm-content": { caretColor: "var(--label)" },
  ".cm-gutters": { backgroundColor: "var(--surface)", color: "var(--quaternary-label)", border: "none" },
  ".cm-activeLineGutter": { backgroundColor: "transparent", color: "var(--secondary-label)" },
  ".cm-activeLine": { backgroundColor: "var(--hover)" },
  "&.cm-focused": { outline: "none" },
  "&.cm-focused .cm-selectionBackground, .cm-selectionBackground, ::selection": {
    backgroundColor: "color-mix(in oklch, var(--accent) 28%, transparent) !important",
  },
  ".cm-searchMatch": { backgroundColor: "color-mix(in oklch, var(--color-warn) 30%, transparent)" },
  ".cm-panels": { backgroundColor: "var(--raised)", color: "var(--label)", borderColor: "var(--separator)" },
  ".cm-foldGutter span": { color: "var(--tertiary-label)" },
});

/** Everything an editor has, bar its document and language. */
export function editorExtensions(onSave: () => void): Extension[] {
  return [
    lineNumbers(),
    highlightActiveLineGutter(),
    foldGutter(),
    highlightSpecialChars(),
    history(),
    drawSelection(),
    indentOnInput(),
    bracketMatching(),
    highlightActiveLine(),
    highlightSelectionMatches(),
    syntaxHighlighting(highlight),
    theme,
    keymap.of([
      {
        key: "Mod-s",
        preventDefault: true,
        run: () => {
          onSave();
          return true;
        },
      },
      indentWithTab,
      ...defaultKeymap,
      ...historyKeymap,
      ...searchKeymap,
    ]),
  ];
}
