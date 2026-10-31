// Removing pier from the account, step by step as pier narrates it.
import { useQueryClient } from "@tanstack/react-query";
import { CircleAlert, CircleCheck } from "lucide-react";
import { useEffect, useState } from "react";
import { rpc } from "@/api/client";
import { Button } from "@/ui/Button";
import { Sheet } from "@/ui/Sheet";
import { Spinner } from "@/ui/Spinner";

export function TeardownSheet({ open, onClose }: { open: boolean; onClose: () => void }) {
  const qc = useQueryClient();
  const [steps, setSteps] = useState<string[]>([]);
  const [state, setState] = useState<"running" | "done" | "failed">("running");
  const [error, setError] = useState("");
  useEffect(() => {
    if (!open) return;
    setSteps([]);
    setState("running");
    setError("");
    let live = true;
    rpc
      .call("account.teardown", {}, { onProgress: (e) => live && setSteps((s) => [...s, e.message]) })
      .then(() => live && setState("done"))
      .catch((e: Error) => {
        if (!live) return;
        setState("failed");
        setError(e.message);
      })
      .finally(() => void qc.invalidateQueries());
    return () => {
      live = false;
    };
  }, [open, qc]);
  return (
    <Sheet
      open={open}
      onOpenChange={(o) => !o && state !== "running" && onClose()}
      title="Removing pier from the account"
      width={560}
    >
      <ol className="flex flex-col gap-1.5 text-sm">
        {steps.map((m, i) => (
          <li key={`${i}-${m}`} className="flex items-center gap-2">
            {i < steps.length - 1 || state === "done" ? (
              <CircleCheck size={14} className="shrink-0 text-ok" />
            ) : state === "running" ? (
              <Spinner />
            ) : (
              <CircleAlert size={14} className="shrink-0 text-bad" />
            )}
            <span className="selectable">{m}</span>
          </li>
        ))}
        {steps.length === 0 && state === "running" && (
          <li className="flex items-center gap-2 text-secondary">
            <Spinner /> Starting…
          </li>
        )}
      </ol>
      {state === "failed" && (
        <div className="selectable mt-3 rounded-card border border-bad/40 bg-bad/8 p-3 text-sm">{error}</div>
      )}
      <div className="mt-4 flex justify-end">
        <Button variant="primary" isDisabled={state === "running"} onPress={onClose}>
          {state === "running" ? "Working…" : "Done"}
        </Button>
      </div>
    </Sheet>
  );
}
