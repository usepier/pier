import { useState } from "react";
import { rpc } from "@/api/client";
import type { Session } from "@/api/protocol.gen";
import { plural } from "@/lib/format";
import { attempt, toast } from "@/state/toasts";
import { Button } from "@/ui/Button";
import { Sheet } from "@/ui/Sheet";

/** Commits everything in the session's checkout, and pushes if asked. */
export function CommitSheet({
  open,
  onOpenChange,
  session: s,
  files,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  session: Session;
  files: number;
}) {
  const [message, setMessage] = useState("");
  // Which button's commit runs: it shows so, the other waits.
  const [busy, setBusy] = useState<"commit" | "push">();
  const commit = async (push: boolean) => {
    if (busy) return;
    setBusy(push ? "push" : "commit");
    const head = await attempt(() =>
      rpc.call("git.commit", { sessionId: s.id, opId: crypto.randomUUID(), message: message.trim() }),
    );
    let pushed = false;
    if (head && push) {
      pushed =
        (await attempt(() => rpc.call("git.push", { sessionId: s.id, opId: crypto.randomUUID() }))) !==
        undefined;
    }
    setBusy(undefined);
    if (!head) return;
    toast(pushed ? "Committed and pushed" : `Committed ${head.head.slice(0, 7)}`, "ok");
    setMessage("");
    onOpenChange(false);
  };
  return (
    <Sheet open={open} onOpenChange={onOpenChange} title={`Commit ${plural(files, "file")}`}>
      <textarea
        autoFocus
        value={message}
        onChange={(e) => setMessage(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === "Enter" && (e.metaKey || e.ctrlKey) && message.trim()) void commit(false);
        }}
        placeholder="What this commit does"
        aria-label="Commit message"
        rows={4}
        className="selectable block w-full resize-none rounded-ctl border border-separator bg-control px-2.5 py-2 text-base outline-none placeholder:text-tertiary focus:border-accent focus:ring-2 focus:ring-accent/25"
      />
      <div className="mt-1 text-xs text-secondary">
        Everything in the checkout goes in, untracked files included.
      </div>
      <div className="mt-4 flex justify-end gap-2">
        <Button onPress={() => onOpenChange(false)}>Cancel</Button>
        <Button
          isPending={busy === "push"}
          isDisabled={busy === "commit" || !message.trim()}
          onPress={() => void commit(true)}
        >
          {busy === "push" ? "Committing and pushing…" : "Commit and Push"}
        </Button>
        <Button
          variant="primary"
          isPending={busy === "commit"}
          isDisabled={busy === "push" || !message.trim()}
          onPress={() => void commit(false)}
        >
          {busy === "commit" ? "Committing…" : "Commit"}
        </Button>
      </div>
    </Sheet>
  );
}
