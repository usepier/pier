import { ShieldQuestion, SquareTerminal } from "lucide-react";
import { useState } from "react";
import { rpc } from "@/api/client";
import type { PermissionRequest } from "@/api/protocol.gen";
import { attempt } from "@/state/toasts";
import { Button } from "@/ui/Button";

// Tools whose answer is more than yes or no: they are answered in the
// agent's own terminal.
const terminalOnly = new Set(["AskUserQuestion", "ExitPlanMode"]);

function pretty(json?: string): string {
  if (!json) return "";
  try {
    return JSON.stringify(JSON.parse(json), null, 2);
  } catch {
    return json;
  }
}

/** The agent asking to use a tool, pinned above the composer until answered. */
export function PermissionCard({
  sessionId,
  req,
  agentName,
  onTerminal,
}: {
  sessionId: string;
  req: PermissionRequest;
  agentName: string;
  onTerminal?: () => void;
}) {
  const [details, setDetails] = useState(false);
  const [busy, setBusy] = useState(false);
  const decide = async (behavior: "allow" | "deny") => {
    setBusy(true);
    await attempt(() => rpc.call("chat.decide", { sessionId, requestId: req.id, behavior }));
    setBusy(false);
  };
  const inTerminal = terminalOnly.has(req.tool);
  return (
    <div className="mx-auto mb-2 w-full max-w-3xl rounded-card border border-warn/45 bg-warn/8 px-3.5 py-3">
      <div className="flex items-start gap-2.5">
        <ShieldQuestion size={16} className="mt-0.5 shrink-0 text-warn" />
        <div className="min-w-0 flex-1">
          <div className="text-base font-medium">
            {inTerminal ? `${agentName} has a question` : `${agentName} wants to use ${req.tool}`}
          </div>
          {req.summary && req.summary !== req.tool && (
            <div className="selectable mt-1 font-mono text-[11.5px] break-all text-label/85">
              {req.summary}
            </div>
          )}
          {details && req.input && (
            <pre className="selectable mt-2 max-h-56 overflow-auto rounded-ctl bg-raised px-2 py-1.5 font-mono text-[11px] whitespace-pre-wrap break-all">
              {pretty(req.input)}
            </pre>
          )}
        </div>
      </div>
      <div className="mt-2.5 flex items-center gap-2 pl-[26px]">
        {req.input && req.input !== "{}" && (
          <button
            type="button"
            onClick={() => setDetails(!details)}
            className="text-xs text-secondary hover:text-label"
          >
            {details ? "Hide details" : "Details"}
          </button>
        )}
        <div className="flex-1" />
        {inTerminal ? (
          <Button size="sm" icon={<SquareTerminal size={12} />} onPress={onTerminal}>
            Answer in terminal
          </Button>
        ) : (
          <>
            <Button size="sm" isDisabled={busy} onPress={() => void decide("deny")}>
              Deny
            </Button>
            <Button size="sm" variant="primary" isDisabled={busy} onPress={() => void decide("allow")}>
              Allow
            </Button>
          </>
        )}
      </div>
    </div>
  );
}

/** The agent waits on something only its terminal can answer. */
export function TerminalWaitCard({ agentName, onTerminal }: { agentName: string; onTerminal?: () => void }) {
  return (
    <div className="mx-auto mb-2 flex w-full max-w-3xl items-center gap-2.5 rounded-card border border-separator bg-raised px-3.5 py-2.5 text-sm">
      <SquareTerminal size={15} className="shrink-0 text-secondary" />
      <span className="flex-1">{agentName} is waiting for you in its terminal.</span>
      {onTerminal && (
        <Button size="sm" onPress={onTerminal}>
          Open terminal
        </Button>
      )}
    </div>
  );
}
