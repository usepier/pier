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
  onDone,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  session: Session;
  files: number;
  onDone: () => void;
}) {
  const [message, setMessage] = useState("");
  const [busy, setBusy] = useState(false);
  const commit = async (push: boolean) => {
    setBusy(true);
    const head = await attempt(() =>
      rpc.call("git.commit", { sessionId: s.id, opId: crypto.randomUUID(), message: message.trim() }),
    );
    let pushed = false;
    if (head && push) {
      pushed =
        (await attempt(() => rpc.call("git.push", { sessionId: s.id, opId: crypto.randomUUID() }))) !==
        undefined;
    }
    setBusy(false);
    if (!head) return;
    toast(pushed ? "Committed and pushed" : `Committed ${head.head.slice(0, 7)}`, "ok");
    setMessage("");
    onOpenChange(false);
    onDone();
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
        <Button isDisabled={busy || !message.trim()} onPress={() => void commit(true)}>
          Commit and Push
        </Button>
        <Button variant="primary" isDisabled={busy || !message.trim()} onPress={() => void commit(false)}>
          {busy ? "Committing…" : "Commit"}
        </Button>
      </div>
    </Sheet>
  );
}
