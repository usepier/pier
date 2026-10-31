import { openUrl } from "@tauri-apps/plugin-opener";
import { Check, Copy } from "lucide-react";
import { memo, useEffect, useState } from "react";
import ReactMarkdown, { type Components } from "react-markdown";
import remarkGfm from "remark-gfm";
import { inTauri } from "@/api/transport";
import { highlight } from "@/lib/highlight";

/** Opens a link in the browser, never in the app's own view. */
export function open(href: string) {
  if (inTauri) void openUrl(href);
  else window.open(href, "_blank", "noopener");
}

/** A code block, highlighted, with a copy button. */
export function CodeBlock({ code, lang }: { code: string; lang?: string }) {
  const [copied, setCopied] = useState(false);
  const [html, setHtml] = useState<string | null>(null);
  useEffect(() => {
    let live = true;
    void highlight(code, lang).then((h) => {
      if (live) setHtml(h);
    });
    return () => {
      live = false;
    };
  }, [code, lang]);
  return (
    <div className="group relative my-2.5 overflow-hidden rounded-card border border-separator bg-raised">
      <div className="flex h-7 items-center border-b border-separator px-3 text-xs text-tertiary">
        <span className="flex-1 font-mono">{lang || "text"}</span>
        <button
          type="button"
          aria-label="Copy code"
          onClick={() => {
            void navigator.clipboard.writeText(code);
            setCopied(true);
            setTimeout(() => setCopied(false), 1200);
          }}
          className="flex h-5 items-center gap-1 rounded-[5px] px-1.5 text-tertiary opacity-0 transition-opacity hover:bg-hover hover:text-label group-hover:opacity-100"
        >
          {copied ? <Check size={12} /> : <Copy size={12} />}
          {copied ? "Copied" : "Copy"}
        </button>
      </div>
      {html ? (
        <div
          className="selectable overflow-x-auto px-3.5 py-2.5 text-[12.5px] leading-[1.55] [&_pre]:!bg-transparent"
          // biome-ignore lint/security/noDangerouslySetInnerHtml: Shiki's output, from the code's own text
          dangerouslySetInnerHTML={{ __html: html }}
        />
      ) : (
        <pre className="selectable overflow-x-auto px-3.5 py-2.5 font-mono text-[12.5px] leading-[1.55]">
          <code>{code}</code>
        </pre>
      )}
    </div>
  );
}

export const components: Components = {
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
  pre: ({ children }) => <>{children}</>,
  code: ({ className, children }) => {
    const lang = /language-(\w+)/.exec(className ?? "")?.[1];
    const text = String(children ?? "");
    if (lang || text.includes("\n")) return <CodeBlock code={text.replace(/\n$/, "")} lang={lang} />;
    return (
      <code className="rounded-[4px] border border-separator bg-raised px-1 py-px font-mono text-[0.88em]">
        {children}
      </code>
    );
  },
  p: ({ children }) => <p className="my-1.5 first:mt-0 last:mb-0">{children}</p>,
  ul: ({ children }) => <ul className="my-1.5 list-disc pl-5">{children}</ul>,
  ol: ({ children }) => <ol className="my-1.5 list-decimal pl-5">{children}</ol>,
  li: ({ children }) => <li className="my-0.5">{children}</li>,
  h1: ({ children }) => <h3 className="mt-3 mb-1 text-md font-semibold">{children}</h3>,
  h2: ({ children }) => <h3 className="mt-3 mb-1 text-md font-semibold">{children}</h3>,
  h3: ({ children }) => <h4 className="mt-2.5 mb-1 font-semibold">{children}</h4>,
  h4: ({ children }) => <h4 className="mt-2 mb-1 font-semibold">{children}</h4>,
  blockquote: ({ children }) => (
    <blockquote className="my-1.5 border-l-2 border-separator pl-3 text-secondary">{children}</blockquote>
  ),
  hr: () => <hr className="my-3 border-separator" />,
  table: ({ children }) => (
    <div className="my-2 overflow-x-auto">
      <table className="text-sm">{children}</table>
    </div>
  ),
  th: ({ children }) => (
    <th className="border border-separator px-2 py-1 text-left font-semibold">{children}</th>
  ),
  td: ({ children }) => <td className="border border-separator px-2 py-1 align-top">{children}</td>,
};

/** The agent's text: GitHub-flavoured markdown, selectable. */
export const Markdown = memo(function Markdown({ text }: { text: string }) {
  return (
    <div className="md selectable text-[14px] leading-[1.6] break-words text-label">
      <ReactMarkdown remarkPlugins={[remarkGfm]} components={components}>
        {text}
      </ReactMarkdown>
    </div>
  );
});
