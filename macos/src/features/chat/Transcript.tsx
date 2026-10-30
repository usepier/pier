import { useVirtualizer } from "@tanstack/react-virtual";
import { ArrowDown, Brain, ChevronRight, ClipboardList, Paperclip, TriangleAlert } from "lucide-react";
import { memo, type ReactNode, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import type { ChatItem } from "@/api/protocol.gen";
import { isOpen, useOpen } from "@/state/disclosure";
import { usePrefs } from "@/state/prefs";
import { Reveal } from "@/ui/Reveal";
import { Spinner } from "@/ui/Spinner";
import { anyFailed, type Block, groupItems, summarize } from "./group";
import { Markdown } from "./Markdown";
import { ToolView, toolLine } from "./ToolView";

/** How close to the bottom still counts as following along. */
const stickPx = 48;

type Row = Block | { kind: "streaming"; item: ChatItem };

// Text being written keeps its row once written (same key, same view), so
// finishing it doesn't redraw it.
function rowKey(r: Row): string {
  if (r.kind === "steps") return `steps-${r.id}`;
  return r.item.id;
}

/** A first guess at a row's height, before it is measured. */
function estimate(r: Row): number {
  if (r.kind === "steps") return 32;
  const i = r.item;
  if (i.kind === "tool") return 30;
  if (i.kind === "status") return 28;
  const lines = Math.ceil((i.text?.length ?? 0) / 90) + (i.text?.split("\n").length ?? 1);
  return Math.min(900, 28 + lines * 22);
}

/**
 * The conversation, oldest at the top, virtualized (only what is near the
 * view is drawn, so a long chat stays fast). It follows new items and the
 * text being written while you are at the bottom, holds still while you
 * read further up, and pages older items in when you scroll to the top.
 */
export function Transcript({
  items,
  streaming,
  more,
  loadingOlder,
  onLoadOlder,
  footer,
  empty,
}: {
  items: ChatItem[];
  /** Items being written, as far as they got. */
  streaming: ChatItem[];
  more: boolean;
  loadingOlder: boolean;
  onLoadOlder: () => void;
  /** After the last item: prompts on their way, the working line. */
  footer?: ReactNode;
  empty?: ReactNode;
}) {
  const scroller = useRef<HTMLDivElement>(null);
  const content = useRef<HTMLDivElement>(null);
  const atBottom = useRef(true);
  const fromBottom = useRef(0);
  const [showJump, setShowJump] = useState(false);
  const expand = usePrefs((p) => p.expandTools);
  const rows = useMemo<Row[]>(() => {
    const blocks = groupItems(items, expand);
    return [
      ...blocks,
      ...streaming.filter((s) => s.text).map((item) => ({ kind: "streaming" as const, item })),
    ];
  }, [items, streaming, expand]);
  const firstId = items[0]?.id;
  const prevFirst = useRef(firstId);

  const v = useVirtualizer({
    count: rows.length,
    getScrollElement: () => scroller.current,
    estimateSize: (i) => estimate(rows[i]!),
    getItemKey: (i) => rowKey(rows[i]!),
    overscan: 8,
    paddingStart: 16,
  });

  // Older items went in at the top: keep what was in view where it was.
  // Otherwise, following along: stay at the bottom.
  useLayoutEffect(() => {
    const el = scroller.current;
    if (!el) return;
    if (prevFirst.current !== firstId && prevFirst.current !== undefined && !atBottom.current) {
      el.scrollTop = el.scrollHeight - el.clientHeight - fromBottom.current;
    } else if (atBottom.current) {
      el.scrollTop = el.scrollHeight;
    }
    prevFirst.current = firstId;
  });

  // Rows measured, text streaming in, a tool opening: the content grows on
  // its own; keep the view pinned while following.
  useEffect(() => {
    const el = scroller.current;
    const c = content.current;
    if (!el || !c) return;
    const ro = new ResizeObserver(() => {
      if (atBottom.current) el.scrollTop = el.scrollHeight;
    });
    ro.observe(c);
    return () => ro.disconnect();
  }, []);

  const onScroll = () => {
    const el = scroller.current!;
    const gap = el.scrollHeight - el.clientHeight - el.scrollTop;
    atBottom.current = gap < stickPx;
    fromBottom.current = gap;
    setShowJump(gap > 400);
    if (el.scrollTop < 400 && more && !loadingOlder) onLoadOlder();
  };

  return (
    <div className="relative min-h-0 flex-1">
      <div ref={scroller} onScroll={onScroll} className="h-full overflow-y-auto" data-testid="transcript">
        <div ref={content}>
          {more && (
            <div className="flex h-8 items-center justify-center text-sm text-tertiary">
              {loadingOlder ? <Spinner /> : "Scroll up for earlier messages"}
            </div>
          )}
          {rows.length === 0 && empty}
          <div className="relative w-full" style={{ height: v.getTotalSize() }}>
            {v.getVirtualItems().map((vi) => {
              const r = rows[vi.index]!;
              return (
                <div
                  key={vi.key}
                  data-index={vi.index}
                  ref={v.measureElement}
                  className="absolute top-0 left-0 w-full"
                  style={{ transform: `translateY(${vi.start}px)` }}
                >
                  <div className="mx-auto max-w-3xl px-6 pb-3">
                    <RowView row={r} />
                  </div>
                </div>
              );
            })}
          </div>
          <div className="mx-auto flex max-w-3xl flex-col gap-3 px-6 pb-5">{footer}</div>
        </div>
      </div>
      {showJump && (
        <button
          type="button"
          onClick={() => {
            const el = scroller.current!;
            el.scrollTo({ top: el.scrollHeight, behavior: "smooth" });
          }}
          className="pop-in absolute bottom-3 left-1/2 flex h-7 -translate-x-1/2 items-center gap-1 rounded-full border border-strong bg-popover px-3 text-sm text-secondary shadow-md transition-colors hover:text-label"
        >
          <ArrowDown size={12} /> Latest
        </button>
      )}
    </div>
  );
}

const RowView = memo(function RowView({ row }: { row: Row }) {
  if (row.kind === "steps") return <Steps id={row.id} items={row.items} live={row.live} />;
  return <ItemView item={row.item} live={row.kind === "streaming"} />;
});

/**
 * A run of steps: one line — what the step at work is doing while it
 * runs, what they did once done — that opens onto them. A single step is
 * just its own line. Steps joining it change the line, not the layout.
 */
function Steps({ id, items, live }: { id: string; items: ChatItem[]; live: boolean }) {
  // A step opened on its own stays open as others join it.
  const [open, setOpen] = useOpen(`steps-${id}`, isOpen(id));
  if (items.length === 1) return <ItemView item={items[0]!} />;
  const at = live ? items.findLast((i) => i.tool?.status === "running") : undefined;
  const line = at?.tool && toolLine(at.tool);
  return (
    <div className="text-sm">
      <button
        type="button"
        aria-expanded={open}
        onClick={() => setOpen(!open)}
        className="-ml-1.5 flex max-w-full min-w-0 items-center gap-1.5 rounded-[6px] px-1.5 py-1 text-left text-secondary transition-colors hover:bg-hover hover:text-label"
      >
        <ChevronRight
          size={12}
          className={`shrink-0 transition-transform duration-200 ${open ? "rotate-90" : ""}`}
        />
        {line ? (
          <>
            <Spinner size={11} />
            <span className="shrink-0">{line.verb}</span>
            {line.summary && (
              <span className="min-w-0 truncate font-mono text-[11.5px] text-label/80">{line.summary}</span>
            )}
            <span className="shrink-0 text-tertiary tabular-nums">· {items.length} steps</span>
          </>
        ) : (
          <>
            <span className="min-w-0 truncate">{summarize(items)}</span>
            {anyFailed(items) && <TriangleAlert size={12} className="shrink-0 text-warn" />}
          </>
        )}
      </button>
      <Reveal open={open}>
        <div className="mt-1 ml-[5px] flex flex-col gap-0.5 border-l border-separator pl-3">
          {items.map((i) => (
            <ItemView key={i.id} item={i} />
          ))}
        </div>
      </Reveal>
    </div>
  );
}

function PlanItem({ text, live }: { text: string; live?: boolean }) {
  return (
    <div className="rounded-card border border-separator bg-raised">
      <div className="flex h-8 items-center gap-1.5 border-b border-separator px-3 text-sm text-secondary">
        <ClipboardList size={13} /> Plan
        {live && <Spinner size={10} />}
      </div>
      <div className="px-4 py-3">
        <Markdown text={text} />
      </div>
    </div>
  );
}

/** One item; live while the agent is still writing it. */
export function ItemView({ item: i, live = false }: { item: ChatItem; live?: boolean }) {
  const body = (() => {
    switch (i.kind) {
      case "user":
        return (
          <div className="flex justify-end pt-2">
            <div className="selectable max-w-[85%] rounded-[12px] border border-separator bg-raised px-3.5 py-2 text-[14px] leading-[1.55] whitespace-pre-wrap break-words">
              {i.text}
              {(i.attachments?.length ?? 0) > 0 && (
                <div className="mt-1.5 flex flex-wrap gap-1.5">
                  {i.attachments!.map((a, n) => (
                    <span
                      key={n}
                      className="inline-flex h-6 items-center gap-1 rounded-[6px] border border-separator bg-surface px-2 text-sm text-secondary"
                    >
                      <Paperclip size={11} /> {a.name}
                    </span>
                  ))}
                </div>
              )}
            </div>
          </div>
        );
      case "assistant":
        return (
          <div className={live ? "typing-caret-last" : undefined}>
            <Markdown text={i.text ?? ""} />
          </div>
        );
      case "thinking":
        return live ? (
          <div className="flex items-start gap-2 text-sm text-tertiary">
            <Brain size={13} className="mt-0.5 shrink-0" />
            <span className="line-clamp-3 italic">{i.text}</span>
          </div>
        ) : (
          <Thinking id={i.id} text={i.text ?? ""} />
        );
      case "plan":
        return <PlanItem text={i.text ?? ""} live={live} />;
      case "tool":
        return i.tool ? <ToolView id={i.id} tool={i.tool} /> : null;
      case "status":
        return (
          <div className="flex items-center gap-3 py-1 text-sm text-tertiary">
            <div className="h-px flex-1 bg-separator" />
            {i.text}
            <div className="h-px flex-1 bg-separator" />
          </div>
        );
      case "error":
        return (
          <div className="selectable rounded-card border border-bad/30 bg-bad/8 px-3.5 py-2.5 text-sm whitespace-pre-wrap text-bad">
            {i.text}
          </div>
        );
      default:
        return null;
    }
  })();
  if (!body) return null;
  return i.sidechain ? <div className="border-l-2 border-separator pl-3 opacity-80">{body}</div> : body;
}

function Thinking({ id, text }: { id: string; text: string }) {
  const [open, setOpen] = useOpen(id);
  return (
    <div className="text-sm">
      <button
        type="button"
        aria-expanded={open}
        onClick={() => setOpen(!open)}
        className="-ml-1.5 flex items-center gap-1.5 rounded-[6px] px-1.5 py-1 text-tertiary transition-colors hover:bg-hover hover:text-secondary"
      >
        <Brain size={13} /> Thought
        <ChevronRight size={11} className={`transition-transform duration-200 ${open ? "rotate-90" : ""}`} />
      </button>
      <Reveal open={open}>
        <div className="selectable mt-1 ml-5 whitespace-pre-wrap text-secondary italic">{text}</div>
      </Reveal>
    </div>
  );
}
