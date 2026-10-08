import { useQuery } from "@tanstack/react-query";
import { Eye, EyeOff } from "lucide-react";
import { useEffect, useState } from "react";
import { rpc } from "@/api/client";
import { Spinner } from "@/ui/Spinner";

function elapsed(since: string, now: number): string {
  const s = Math.max(0, Math.floor((now - Date.parse(since)) / 1000));
  if (s < 60) return `${s}s`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m ${s % 60}s`;
  return `${Math.floor(m / 60)}h ${m % 60}m`;
}

/**
 * The agent is mid-turn. Peek shows the bottom of its pane, read once a
 * second — a look, not an attach, so it never keeps the session awake.
 */
export function Working({ sessionId, since, live }: { sessionId: string; since: string; live: boolean }) {
  const [now, setNow] = useState(Date.now());
  const [peek, setPeek] = useState(false);
  useEffect(() => {
    const t = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(t);
  }, []);
  const pane = useQuery({
    queryKey: ["peek", sessionId],
    queryFn: () => rpc.call("chat.peek", { sessionId, lines: 24 }),
    enabled: peek && live,
    refetchInterval: 1000,
  });
  return (
    <div className="flex flex-col gap-1.5">
      <div className="flex items-center gap-2 text-xs text-secondary">
        <Spinner size={11} />
        <span className="tabular-nums">Working · {elapsed(since, now)}</span>
        {live && (
          <button
            type="button"
            onClick={() => setPeek(!peek)}
            className="ml-1 inline-flex items-center gap-1 rounded-ctl px-1.5 py-0.5 text-tertiary hover:bg-hover hover:text-label"
          >
            {peek ? <EyeOff size={11} /> : <Eye size={11} />} {peek ? "Hide" : "Peek"}
          </button>
        )}
      </div>
      {peek && (
        <pre className="selectable max-h-64 overflow-auto rounded-ctl bg-raised px-2.5 py-1.5 font-mono text-[11px] leading-[1.4] whitespace-pre text-secondary">
          {pane.data?.text || (pane.isLoading ? "…" : "")}
        </pre>
      )}
    </div>
  );
}
