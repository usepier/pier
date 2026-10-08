import { useQuery } from "@tanstack/react-query";
import { useParams } from "@tanstack/react-router";
import { KeyRound, Power } from "lucide-react";
import { useState } from "react";
import { Group, Panel, Separator, useDefaultLayout } from "react-resizable-panels";
import { rpc } from "@/api/client";
import type { Session } from "@/api/protocol.gen";
import { JobProgress } from "@/features/jobs/JobProgress";
import { age } from "@/lib/format";
import { useRunningJobs } from "@/state/jobs";
import { useSession } from "@/state/overview";
import { attempt } from "@/state/toasts";
import { Banner } from "@/ui/Banner";
import { Button } from "@/ui/Button";
import { Empty } from "@/ui/Empty";
import { LogView } from "@/ui/LogView";
import { Facts } from "@/ui/Panel";
import { Spinner } from "@/ui/Spinner";
import { EnvironmentPanel } from "./EnvironmentPanel";
import { SessionCenter } from "./SessionCenter";
import { SessionHeader } from "./SessionHeader";

type InspectorTab = "env" | "log" | "details";

export function SessionPage() {
  const { id } = useParams({ from: "/session/$id" });
  const s = useSession(id);
  const [resizeJob, setResizeJob] = useState<string>();
  const [tab, setTab] = useState<InspectorTab>("env");
  const [openWindow, setOpenWindow] = useState<string>();
  const layout = useDefaultLayout({ id: "pier.session", storage: localStorage });
  const resizing = useRunningJobs((j) => j.kind === "resize" && j.session === s?.name)[0]?.id ?? resizeJob;
  if (!s) return <Empty title="Session" detail="Gone, or still loading." />;
  const live = ["running", "working", "idle"].includes(s.state);
  return (
    <div className="flex h-full flex-col">
      <SessionHeader session={s} onResize={setResizeJob} />
      {!s.hasKey && s.state !== "failed" && (
        <Banner tone="warn">
          <span className="inline-flex items-center gap-1.5">
            <KeyRound size={13} /> Created on another machine — its key lives there, so this Mac can see it
            but not reach it.
          </span>
        </Banner>
      )}
      {resizing && (
        <div className="border-b border-separator px-5 py-2">
          <JobProgress id={resizing} compact />
        </div>
      )}
      <Group orientation="horizontal" className="min-h-0 flex-1" {...layout}>
        <Panel id="center" minSize={360} className="min-w-0">
          {live && s.hasKey ? <SessionCenter session={s} openWindow={openWindow} /> : <Idle session={s} />}
        </Panel>
        <Separator className="w-px bg-separator outline-none data-[separator=active]:bg-accent" />
        <Panel id="inspector" defaultSize={340} minSize={260} maxSize={720} className="flex min-w-0 flex-col">
          <div role="tablist" className="flex h-9 shrink-0 items-center gap-1 border-b border-separator px-2">
            {(
              [
                ["env", "Environment"],
                ["log", s.state === "failed" ? "Create log" : "Setup log"],
                ["details", "Details"],
              ] as [InspectorTab, string][]
            ).map(([k, title]) => (
              <button
                key={k}
                type="button"
                role="tab"
                aria-selected={tab === k}
                onClick={() => setTab(k)}
                className={`h-7 rounded-ctl px-2.5 text-sm ${tab === k ? "bg-selected text-label" : "text-secondary hover:bg-hover"}`}
              >
                {title}
              </button>
            ))}
          </div>
          <div className="min-h-0 flex-1">
            {tab === "env" && (
              <EnvironmentPanel
                session={s}
                onOpenWindow={(w) => {
                  setOpenWindow(w);
                  setTimeout(() => setOpenWindow(undefined), 0);
                }}
              />
            )}
            {tab === "log" && <SetupLog session={s} />}
            {tab === "details" && <Details session={s} />}
          </div>
        </Panel>
      </Group>
    </div>
  );
}

/** The center while there is no live terminal to show. */
function Idle({ session: s }: { session: Session }) {
  const [waking, setWaking] = useState(false);
  if (s.state === "parked") {
    return (
      <Empty
        icon={<Power size={26} strokeWidth={1.5} />}
        title="Parked"
        detail="Nothing runs and nothing bills but the disk. Waking brings its windows and agent conversations back, in about 20 seconds to a minute."
      >
        <Button
          variant="primary"
          isDisabled={waking || !s.hasKey}
          onPress={async () => {
            setWaking(true);
            await attempt(() => rpc.call("sessions.resume", { id: s.id }));
            setWaking(false);
          }}
        >
          {waking ? "Waking…" : "Wake"}
        </Button>
      </Empty>
    );
  }
  if (s.state === "creating")
    return (
      <Empty icon={<Spinner size={20} />} title="Setting up" detail="It opens here as soon as it's ready." />
    );
  if (s.state === "failed") return <Empty title="The create failed" detail={s.failReason} />;
  if (s.state === "deleting") return <Empty title="Being removed" />;
  return <Empty title="Not reachable from this Mac" />;
}

function Details({ session: s }: { session: Session }) {
  return (
    <div className="p-3">
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
  if (!readable) {
    return (
      <div className="p-4 text-sm text-secondary">
        {s.state === "parked" ? "Parked — wake it to read its setup log." : "Not readable yet."}
      </div>
    );
  }
  if (log.error) return <div className="selectable p-4 text-sm text-bad">{(log.error as Error).message}</div>;
  return (
    <LogView
      text={log.data?.text ?? ""}
      className="h-full rounded-none"
      empty={log.isLoading ? "reading…" : "no setup log"}
    />
  );
}
