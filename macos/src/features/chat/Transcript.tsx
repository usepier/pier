import { ArrowDown, Brain, ChevronRight, Image, TriangleAlert } from "lucide-react";
import { memo, type ReactNode, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import type { ChatItem } from "@/api/protocol.gen";
import { Spinner } from "@/ui/Spinner";
import { anyFailed, type Block, groupItems, summarize } from "./group";
import { Markdown } from "./Markdown";
import { ToolView } from "./ToolView";

/** How close to the bottom still counts as following along. */
const stickPx = 48;

/**
 * The conversation, oldest at the top. It follows new items while you are
 * at the bottom, holds still while you read further up, and pages older
 * items in when you scroll to the top.
 */
export function Transcript({
  items,
  more,
  loadingOlder,
  onLoadOlder,
  footer,
  empty,
}: {
  items: ChatItem[];
  more: boolean;
  loadingOlder: boolean;
  onLoadOlder: () => void;
  /** After the last item: prompts on their way, the working line. */
  footer?: ReactNode;
  empty?: ReactNode;
}) {
  const scroller = useRef<HTMLDivElement>(null);
  const atBottom = useRef(true);
  const [showJump, setShowJump] = useState(false);
  const firstId = items[0]?.id;
  const prevFirst = useRef(firstId);
  const fromBottom = useRef(0);
  const blocks = useMemo(() => groupItems(items), [items]);

  // Older items went in at the top: keep what was in view where it was.
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

  // Content that grows on its own (a tool opening, an image) keeps the view
  // pinned while following.
  useEffect(() => {
    const el = scroller.current;
    const content = el?.firstElementChild;
    if (!el || !content) return;
    const ro = new ResizeObserver(() => {
      if (atBottom.current) el.scrollTop = el.scrollHeight;
    });
    ro.observe(content);
    return () => ro.disconnect();
  }, []);

  const onScroll = () => {
    const el = scroller.current!;
    const gap = el.scrollHeight - el.clientHeight - el.scrollTop;
    atBottom.current = gap < stickPx;
    fromBottom.current = gap;
    setShowJump(gap > 400);
    if (el.scrollTop < 300 && more && !loadingOlder) onLoadOlder();
  };

  return (
    <div className="relative min-h-0 flex-1">
      <div ref={scroller} onScroll={onScroll} className="h-full overflow-y-auto">
        <div className="mx-auto flex max-w-3xl flex-col gap-3 px-5 py-4">
          {more && (
            <div className="flex h-6 items-center justify-center text-xs text-tertiary">
              {loadingOlder ? <Spinner /> : "Scroll up for earlier messages"}
            </div>
          )}
          {items.length === 0 && empty}
          {blocks.map((b) => (
            <BlockView key={b.kind === "item" ? b.item.id : `steps-${b.id}`} block={b} />
          ))}
          {footer}
        </div>
      </div>
      {showJump && (
        <button
          type="button"
          onClick={() => {
            const el = scroller.current!;
            el.scrollTo({ top: el.scrollHeight, behavior: "smooth" });
          }}
          className="absolute bottom-3 left-1/2 flex h-7 -translate-x-1/2 items-center gap-1 rounded-full border border-separator bg-control px-3 text-xs shadow-md hover:bg-hover"
        >
          <ArrowDown size={12} /> Latest
        </button>
      )}
    </div>
  );
}

const BlockView = memo(function BlockView({ block }: { block: Block }) {
  if (block.kind === "steps") return <Steps items={block.items} />;
  return <ItemView item={block.item} />;
});

function Steps({ items }: { items: ChatItem[] }) {
  const [open, setOpen] = useState(false);
  return (
    <div className="text-sm">
      <button
        type="button"
        aria-expanded={open}
        onClick={() => setOpen(!open)}
        className="flex items-center gap-1.5 rounded-ctl py-0.5 text-secondary hover:text-label"
      >
        <ChevronRight size={12} className={`transition-transform ${open ? "rotate-90" : ""}`} />
        {summarize(items)}
        {anyFailed(items) && <TriangleAlert size={12} className="text-warn" />}
      </button>
      {open && (
        <div className="mt-1 flex flex-col gap-1 border-l border-separator pl-3">
          {items.map((i) => (
            <ItemView key={i.id} item={i} />
          ))}
        </div>
      )}
    </div>
  );
}

function ItemView({ item: i }: { item: ChatItem }) {
  const body = (() => {
    switch (i.kind) {
      case "user":
        return (
          <div className="flex justify-end">
            <div className="selectable max-w-[85%] rounded-[14px] bg-accent-soft px-3 py-2 whitespace-pre-wrap break-words">
              {i.text}
              {(i.images ?? 0) > 0 && (
                <div className="mt-1 flex items-center gap-1 text-xs text-secondary">
                  <Image size={12} /> {i.images === 1 ? "an image" : `${i.images} images`}
                </div>
              )}
            </div>
          </div>
        );
      case "assistant":
        return <Markdown text={i.text ?? ""} />;
      case "thinking":
        return <Thinking text={i.text ?? ""} />;
      case "tool":
        return i.tool ? <ToolView tool={i.tool} /> : null;
      case "status":
        return (
          <div className="flex items-center gap-3 text-xs text-tertiary">
            <div className="h-px flex-1 bg-separator" />
            {i.text}
            <div className="h-px flex-1 bg-separator" />
          </div>
        );
      case "error":
        return <div className="selectable rounded-ctl bg-bad/10 px-3 py-2 text-sm text-bad">{i.text}</div>;
      default:
        return null;
    }
  })();
  if (!body) return null;
  return i.sidechain ? <div className="border-l-2 border-separator pl-3 opacity-80">{body}</div> : body;
}

function Thinking({ text }: { text: string }) {
  const [open, setOpen] = useState(false);
  return (
    <div className="text-sm">
      <button
        type="button"
        aria-expanded={open}
        onClick={() => setOpen(!open)}
        className="flex items-center gap-1.5 text-tertiary hover:text-secondary"
      >
        <Brain size={13} /> Thought
        <ChevronRight size={11} className={`transition-transform ${open ? "rotate-90" : ""}`} />
      </button>
      {open && <div className="selectable mt-1 ml-5 whitespace-pre-wrap text-secondary italic">{text}</div>}
    </div>
  );
}
