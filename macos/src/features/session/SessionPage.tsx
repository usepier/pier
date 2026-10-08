import { useQuery } from "@tanstack/react-query";
import { useParams } from "@tanstack/react-router";
import { KeyRound } from "lucide-react";
import { useState } from "react";
import { rpc } from "@/api/client";
import type { Session } from "@/api/protocol.gen";
import { JobProgress } from "@/features/jobs/JobProgress";
import { age } from "@/lib/format";
import { useRunningJobs } from "@/state/jobs";
import { useSession } from "@/state/overview";
import { Banner } from "@/ui/Banner";
import { Empty } from "@/ui/Empty";
import { LogView } from "@/ui/LogView";
import { Facts, Panel } from "@/ui/Panel";
import { SessionHeader } from "./SessionHeader";

export function SessionPage() {
  const { id } = useParams({ from: "/session/$id" });
  const s = useSession(id);
  const [resizeJob, setResizeJob] = useState<string>();
  const resizing = useRunningJobs((j) => j.kind === "resize" && j.session === s?.name)[0]?.id ?? resizeJob;
  if (!s) return <Empty title="Session" detail="Gone, or still loading." />;
  return (
    <div className="flex h-full flex-col">
      <SessionHeader session={s} onResize={setResizeJob} />
      {!s.hasKey && (
        <Banner tone="warn">
          <span className="inline-flex items-center gap-1.5">
            <KeyRound size={13} /> Created on another machine — its key lives there, so this Mac can see it
            but not reach it.
          </span>
        </Banner>
      )}
      <div className="min-h-0 flex-1 overflow-y-auto">
        <div className="mx-auto flex max-w-5xl flex-col gap-4 p-5">
          {resizing && (
            <Panel title="Resizing">
              <JobProgress id={resizing} />
            </Panel>
          )}
          <div className="grid gap-4 md:grid-cols-[minmax(0,1fr)_minmax(0,1.6fr)]">
            <Panel title="Details">
              <Facts
                rows={[
                  ["State", s.activity || s.label],
                  ["Repository", s.origin || s.repo],
                  ["Branch", s.branch],
                  ["Based on", s.base || "—"],
                  ["Machine", s.machineType],
                  ["Cost", s.costNote],
                  ["Created", s.createdAt ? `${age(s.createdAt)} ago` : "—"],
                  ...(s.failReason ? ([["Failed", s.failReason]] as [string, string][]) : []),
                ]}
              />
            </Panel>
            <SetupLog session={s} />
          </div>
        </div>
      </div>
    </div>
  );
}

function SetupLog({ session: s }: { session: Session }) {
  const readable =
    ["running", "working", "idle", "failed"].includes(s.state) && (s.hasKey || s.state === "failed");
  const log = useQuery({
    queryKey: ["setup-log", s.id],
    queryFn: () => rpc.call("logs.setup", { id: s.id, lines: 2000 }),
    enabled: readable,
    refetchInterval: s.setup === "running" ? 4000 : false,
  });
  return (
    <Panel title={s.state === "failed" ? "Create log" : "Setup log"}>
      {!readable ? (
        <div className="py-6 text-center text-sm text-secondary">
          {s.state === "parked" ? "Parked — wake it to read its setup log." : "Not readable yet."}
        </div>
      ) : log.error ? (
        <div className="selectable text-sm text-bad">{(log.error as Error).message}</div>
      ) : (
        <LogView
          text={log.data?.text ?? ""}
          className="max-h-[420px] min-h-40"
          empty={log.isLoading ? "reading…" : "no setup log"}
        />
      )}
    </Panel>
  );
}
