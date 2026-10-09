import {
  Bot,
  ChevronRight,
  Circle,
  CircleCheck,
  CircleDot,
  ClipboardList,
  FilePlus,
  FileText,
  Globe,
  ListChecks,
  MessageCircleQuestion,
  Pencil,
  Search,
  SquareTerminal,
  Wrench,
} from "lucide-react";
import { useState } from "react";
import type { EditView, ToolCall } from "@/api/protocol.gen";
import { Badge } from "@/ui/Badge";
import { Spinner } from "@/ui/Spinner";
import { Markdown } from "./Markdown";

const icons = {
  bash: SquareTerminal,
  edit: Pencil,
  write: FilePlus,
  read: FileText,
  search: Search,
  web: Globe,
  task: Bot,
  todo: ListChecks,
  ask: MessageCircleQuestion,
  plan: ClipboardList,
  generic: Wrench,
} as const;

const verbs: Record<string, string> = {
  bash: "Ran",
  edit: "Edited",
  write: "Created",
  read: "Read",
  search: "Searched",
  web: "Fetched",
  task: "Delegated",
  todo: "Updated the plan",
  ask: "Asked",
  plan: "Proposed a plan",
};

/** A path relative to the checkout, when it is in one. */
export function shortPath(p: string): string {
  const m = p.match(/^\/home\/[^/]+\/work\/[^/]+\/(.+)$/);
  return m ? m[1]! : p;
}

function Status({ t }: { t: ToolCall }) {
  switch (t.status) {
    case "running":
      return <Spinner size={11} />;
    case "error":
      // A plan or questions declined were the user's answer, not a failure.
      if (t.view === "plan") return <Badge tone="warn">sent back</Badge>;
      if (t.view === "ask") return <Badge>skipped</Badge>;
      return <Badge tone="bad">failed</Badge>;
    case "denied":
      return <Badge tone="warn">denied</Badge>;
    case "interrupted":
      return <Badge>interrupted</Badge>;
    default:
      return null;
  }
}

/** One tool call: a line that opens onto what it did. */
export function ToolView({ tool: t }: { tool: ToolCall }) {
  const [open, setOpen] = useState(t.view === "todo");
  const Icon = icons[t.view as keyof typeof icons] ?? Wrench;
  const verb = verbs[t.view] ?? t.name;
  const summary =
    t.view === "edit" || t.view === "write" || t.view === "read" ? shortPath(t.summary) : t.summary;
  return (
    <div className="text-sm">
      <button
        type="button"
        aria-expanded={open}
        onClick={() => setOpen(!open)}
        className="group flex w-full min-w-0 items-center gap-1.5 rounded-ctl py-0.5 text-left text-secondary hover:text-label"
      >
        <Icon size={13} className="shrink-0" />
        <span className="shrink-0">{verb}</span>
        {t.view !== "todo" && summary && (
          <span className="min-w-0 truncate font-mono text-[11.5px] text-label/80">{summary}</span>
        )}
        {t.edit && <EditCounts edit={t.edit} />}
        <Status t={t} />
        <ChevronRight
          size={11}
          className={`shrink-0 opacity-0 transition-transform group-hover:opacity-100 ${open ? "rotate-90" : ""}`}
        />
      </button>
      {open && (
        <div className="mt-1 mb-2 ml-5">
          <ToolDetail tool={t} />
        </div>
      )}
    </div>
  );
}

function EditCounts({ edit }: { edit: EditView }) {
  let add = 0;
  let del = 0;
  for (const h of edit.hunks ?? [])
    for (const l of h.lines) {
      if (l.startsWith("+")) add++;
      else if (l.startsWith("-")) del++;
    }
  if (!add && !del) return null;
  return (
    <span className="shrink-0 font-mono text-[11px]">
      {add > 0 && <span className="text-ok">+{add}</span>}{" "}
      {del > 0 && <span className="text-bad">−{del}</span>}
    </span>
  );
}

function Pre({ text, className = "" }: { text: string; className?: string }) {
  return (
    <pre
      className={`selectable max-h-80 overflow-auto rounded-ctl bg-raised px-2.5 py-1.5 font-mono text-[11.5px] leading-[1.45] whitespace-pre-wrap break-all ${className}`}
    >
      {text}
    </pre>
  );
}

function ToolDetail({ tool: t }: { tool: ToolCall }) {
  if (t.view === "bash" && t.bash) {
    const b = t.bash;
    return (
      <div className="flex flex-col gap-1">
        {b.description && <div className="text-xs text-tertiary">{b.description}</div>}
        <Pre text={`$ ${b.command}${b.output ? `\n${b.output}` : ""}`} />
        {b.exitCode != null && b.exitCode !== 0 && <div className="text-xs text-bad">exit {b.exitCode}</div>}
      </div>
    );
  }
  if ((t.view === "edit" || t.view === "write") && t.edit) {
    return <Hunks edit={t.edit} />;
  }
  if (t.view === "ask") {
    return (
      <div className="flex flex-col gap-1.5">
        {(t.questions ?? []).map((q) => (
          <div key={q.question}>
            <div className="text-sm text-secondary">{q.question}</div>
            <div className="selectable text-sm">
              {t.answers?.[q.question] ?? (
                <span className="text-tertiary">
                  {t.status === "running" ? "waiting for you" : "not answered"}
                </span>
              )}
            </div>
          </div>
        ))}
      </div>
    );
  }
  if (t.view === "plan") {
    return t.plan ? (
      <div className="rounded-ctl border border-separator px-3 py-2">
        <Markdown text={t.plan} />
      </div>
    ) : (
      <div className="text-xs text-tertiary">{t.result}</div>
    );
  }
  if (t.view === "todo") {
    return (
      <ul className="flex flex-col gap-0.5">
        {(t.todos ?? []).map((td, i) => (
          <li key={i} className="flex items-start gap-1.5 text-sm">
            {td.status === "completed" ? (
              <CircleCheck size={13} className="mt-px shrink-0 text-ok" />
            ) : td.status === "in_progress" ? (
              <CircleDot size={13} className="mt-px shrink-0 text-accent" />
            ) : (
              <Circle size={13} className="mt-px shrink-0 text-tertiary" />
            )}
            <span className={td.status === "completed" ? "text-secondary line-through" : ""}>
              {td.content}
            </span>
          </li>
        ))}
      </ul>
    );
  }
  return (
    <div className="flex flex-col gap-1">
      {t.input && t.input !== "{}" && <Pre text={pretty(t.input)} />}
      {t.result && <Pre text={t.result} className="text-secondary" />}
      {t.truncated && <div className="text-2xs text-tertiary">trimmed — the terminal has all of it</div>}
    </div>
  );
}

function pretty(json: string): string {
  try {
    return JSON.stringify(JSON.parse(json), null, 2);
  } catch {
    return json;
  }
}

/** A file change as its hunks, the way a diff reads. */
export function Hunks({ edit }: { edit: EditView }) {
  if (!edit.hunks?.length) return <div className="text-xs text-tertiary">{shortPath(edit.path)}</div>;
  return (
    <div className="selectable max-h-96 overflow-auto rounded-ctl border border-separator font-mono text-[11.5px] leading-[1.5]">
      {edit.hunks.map((h, hi) => {
        let o = h.oldStart;
        let n = h.newStart;
        return (
          <div key={hi} className={hi ? "border-t border-separator" : ""}>
            {h.lines.map((l, li) => {
              const kind = l[0];
              const on = kind === "-" ? o++ : kind === "+" ? undefined : o++;
              const nn = kind === "+" ? n++ : kind === "-" ? undefined : n++;
              const bg = kind === "+" ? "bg-ok/12" : kind === "-" ? "bg-bad/12" : "";
              return (
                <div key={li} className={`flex ${bg}`}>
                  <span className="w-9 shrink-0 pr-1 text-right text-quaternary">{on ?? ""}</span>
                  <span className="w-9 shrink-0 pr-1 text-right text-quaternary">{nn ?? ""}</span>
                  <span className="w-3 shrink-0 text-tertiary">{kind === " " ? "" : kind}</span>
                  <span className="whitespace-pre-wrap break-all pr-2">{l.slice(1)}</span>
                </div>
              );
            })}
          </div>
        );
      })}
    </div>
  );
}
