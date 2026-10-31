// A comment or review as GitHub renders it — markdown, the HTML people
// and bots write in it (details, tables, images), mentions — kept to
// GitHub's own allow-list (nothing that runs survives), and drawn with
// the app's styles rather than as raw text.
import type { Element, Root, RootContent } from "hast";
import { type Components, toJsxRuntime } from "hast-util-to-jsx-runtime";
import { memo, useMemo } from "react";
import { Fragment, jsx, jsxs } from "react/jsx-runtime";
import rehypeParse from "rehype-parse";
import rehypeSanitize from "rehype-sanitize";
import { unified } from "unified";
import { components as markdown, open } from "@/features/chat/Markdown";

const processor = unified().use(rehypeParse, { fragment: true }).use(rehypeSanitize);

/** A code block's text sits in <pre> itself (GitHub highlights it in spans). */
function unwrapCode(nodes: RootContent[]) {
  for (const n of nodes) {
    if (n.type !== "element") continue;
    const only = n.children.length === 1 ? n.children[0] : undefined;
    if (n.tagName === "pre" && only?.type === "element" && (only as Element).tagName === "code") {
      n.children = (only as Element).children;
    }
    unwrapCode(n.children);
  }
}

const components = {
  ...markdown,
  a: ({ href, children }) => (
    <a
      href={href}
      onClick={(e) => {
        e.preventDefault();
        if (href) open(href);
      }}
      className="text-accent underline-offset-2 hover:underline"
    >
      {children}
    </a>
  ),
  pre: ({ children }) => (
    <pre className="selectable my-2 overflow-x-auto rounded-card border border-separator bg-raised px-3.5 py-2.5 font-mono text-[12.5px] leading-[1.55]">
      {children}
    </pre>
  ),
  code: ({ children }) => (
    <code className="rounded-[4px] border border-separator bg-raised px-1 py-px font-mono text-[0.88em]">
      {children}
    </code>
  ),
  img: ({ src, alt, width, height }) =>
    // An image the allow-list emptied (a script for a source) shows nothing.
    src ? (
      <img
        src={src}
        alt={alt ?? ""}
        width={width}
        height={height}
        loading="lazy"
        referrerPolicy="no-referrer"
        className="inline max-w-full rounded-[4px] align-middle"
      />
    ) : null,
  details: ({ children, open: isOpen }) => (
    <details
      open={isOpen}
      className="my-2 rounded-ctl border border-separator px-3 py-1.5 [&[open]>summary]:mb-1.5"
    >
      {children}
    </details>
  ),
  summary: ({ children }) => (
    <summary className="cursor-pointer text-secondary select-none hover:text-label">{children}</summary>
  ),
} as Components;

export const GitHubHTML = memo(function GitHubHTML({ html }: { html: string }) {
  const content = useMemo(() => {
    const tree = processor.runSync(processor.parse(html)) as Root;
    unwrapCode(tree.children);
    return toJsxRuntime(tree, { Fragment, jsx, jsxs, components });
  }, [html]);
  return <div className="md selectable text-[13.5px] leading-[1.6] break-words text-label">{content}</div>;
});
