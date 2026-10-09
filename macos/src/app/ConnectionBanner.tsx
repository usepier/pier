import { useState } from "react";
import { apiLog, rpc } from "@/api/client";
import { useStatus } from "@/api/live";
import { Banner } from "@/ui/Banner";
import { Button } from "@/ui/Button";

/** Says so when the connection to pier is down, and offers the way back. */
export function ConnectionBanner() {
  const status = useStatus();
  const [showLog, setShowLog] = useState(false);
  if (status === "ready" || status === "starting") return null;
  if (status === "restarting") {
    return <Banner tone="warn">pier stopped — restarting…</Banner>;
  }
  if (status === "incompatible") {
    return (
      <Banner tone="bad">
        {rpc.lastExit}. They ship together and must match — install Pier again to fix it.
      </Banner>
    );
  }
  return (
    <>
      <Banner
        tone="bad"
        actions={
          <>
            <Button size="sm" variant="plain" onPress={() => setShowLog((v) => !v)}>
              {showLog ? "Hide log" : "Show log"}
            </Button>
            <Button size="sm" onPress={() => rpc.restart()}>
              Restart
            </Button>
          </>
        }
      >
        pier keeps stopping: {rpc.lastExit}
      </Banner>
      {showLog && (
        <pre className="selectable max-h-48 overflow-auto border-b border-separator bg-raised px-4 py-2 font-mono text-xs text-secondary">
          {apiLog().join("") || "no output"}
        </pre>
      )}
    </>
  );
}
