import { CircleCheck } from "lucide-react";
import { useEffect, useState } from "react";
import { rpc } from "@/api/client";
import type { LocalPurpose } from "@/api/protocol.gen";
import { TerminalView } from "@/features/terminal/TerminalView";
import { Button } from "@/ui/Button";
import { Sheet } from "@/ui/Sheet";

/** A terminal on this Mac for a step that wants one: a login, a token. */
export function LocalTerminalSheet({
  purpose,
  profile,
  title,
  hint,
  onClose,
  onCaptured,
}: {
  purpose?: LocalPurpose;
  profile?: string;
  title: string;
  hint?: string;
  onClose: () => void;
  /** pier took what the terminal printed (a token) and kept it. */
  onCaptured?: (what: string) => void;
}) {
  const [captured, setCaptured] = useState("");
  useEffect(() => {
    if (!purpose) return;
    setCaptured("");
    return rpc.on("term.captured", (p) => {
      setCaptured(p.what);
      onCaptured?.(p.what);
    });
  }, [purpose, onCaptured]);
  return (
    <Sheet open={!!purpose} onOpenChange={(o) => !o && onClose()} title={title} width={720}>
      {captured === "claude-token" ? (
        <div className="mb-2 flex items-center gap-1.5 text-sm text-ok">
          <CircleCheck size={14} /> Got the token — pier keeps it in its settings. You can close this.
        </div>
      ) : (
        hint && <div className="mb-2 text-sm text-secondary">{hint}</div>
      )}
      <div className="h-[340px] overflow-hidden rounded-ctl border border-separator">
        {purpose && <TerminalView local={purpose} profile={profile} visible />}
      </div>
      <div className="mt-3 flex justify-end">
        <Button variant="primary" onPress={onClose}>
          Done
        </Button>
      </div>
    </Sheet>
  );
}
