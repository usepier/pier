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
}: {
  purpose?: LocalPurpose;
  profile?: string;
  title: string;
  hint?: string;
  onClose: () => void;
}) {
  return (
    <Sheet open={!!purpose} onOpenChange={(o) => !o && onClose()} title={title} width={720}>
      {hint && <div className="mb-2 text-sm text-secondary">{hint}</div>}
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
