import { useQuery } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { Cpu, Pin, Power, Trash2 } from "lucide-react";
import { useEffect, useState } from "react";
import { Button as AriaButton, Dialog, DialogTrigger, Popover } from "react-aria-components";
import { rpc } from "@/api/client";
import type { Session } from "@/api/protocol.gen";
import { Toolbar } from "@/app/toolbar";
import { confirm } from "@/lib/confirm";
import { until } from "@/lib/format";
import { running } from "@/lib/session";
import { trackJob } from "@/state/jobs";
import { sessionTone } from "@/state/overview";
import { attempt, toast } from "@/state/toasts";
import { Badge } from "@/ui/Badge";
import { Button } from "@/ui/Button";
import { Spinner } from "@/ui/Spinner";
import { StateDot } from "@/ui/StateDot";

/** A session's name, where it stands, what it costs, and what you can do to it. */
export function SessionHeader({
  session: s,
  onResize,
}: {
  session: Session;
  onResize: (jobId: string) => void;
}) {
  const navigate = useNavigate();
  const [waking, setWaking] = useState(false);
  const live = running(s);
  const wake = async () => {
    setWaking(true);
    await attempt(() => rpc.call("sessions.resume", { id: s.id }));
    setWaking(false);
  };
  const remove = async () => {
    const what =
      s.state === "failed"
        ? "This dismisses the failed create's record."
        : "The VM and its disk are destroyed. Anything not pushed is lost.";
    if (!(await confirm(`Remove ${s.name}?`, what, { ok: "Remove", destructive: true }))) return;
    await attempt(() => rpc.call("sessions.remove", { id: s.id }));
    toast(`Removing ${s.name}`, "info");
    navigate({ to: "/repo/$name", params: { name: s.repo } });
  };
  return (
    <Toolbar>
      <StateDot tone={sessionTone(s)} className="size-2.5" />
      <div data-tauri-drag-region className="min-w-0 flex-1 leading-tight">
        <div className="flex items-center gap-2">
          <h1 className="truncate text-base font-semibold">{s.name}</h1>
          <Badge>{s.label}</Badge>
          {s.setup === "running" && <Badge tone="info">setting up</Badge>}
          {s.setup === "failed" && <Badge tone="bad">setup failed</Badge>}
          {s.strained && <Badge tone="warn">strained</Badge>}
        </div>
        <div className="selectable truncate text-xs text-secondary">
          {s.branch}
          {s.base && <span className="text-tertiary"> ← {s.base}</span>}
          <span className="text-tertiary">
            {" "}
            · {s.machineType} · {s.costNote}
            <Countdown session={s} />
          </span>
        </div>
      </div>
      <div className="flex shrink-0 items-center gap-2">
        {s.state === "parked" && (
          <Button
            variant="primary"
            icon={waking ? <Spinner className="text-on-accent" /> : <Power size={13} />}
            isDisabled={waking || !s.hasKey}
            onPress={() => void wake()}
          >
            {waking ? "Waking…" : "Wake"}
          </Button>
        )}
        {live && (
          <Button
            icon={<Pin size={13} />}
            onPress={() =>
              attempt(async () => {
                await rpc.call("sessions.keep", { id: s.id });
                toast(`${s.name} won't park when idle`, "ok");
              })
            }
          >
            Keep
          </Button>
        )}
        {(live || s.state === "parked") && <ResizeButton session={s} onResize={onResize} />}
        <Button variant="plain" icon={<Trash2 size={13} />} onPress={() => void remove()}>
          Remove
        </Button>
      </div>
    </Toolbar>
  );
}

/** When the session stops on its own, ticking down. */
function Countdown({ session: s }: { session: Session }) {
  const [, tick] = useState(0);
  useEffect(() => {
    const t = setInterval(() => tick((n) => n + 1), 30_000);
    return () => clearInterval(t);
  }, []);
  const park = until(s.parkAt);
  const cap = until(s.capAt);
  if (park) return <span className="text-tertiary"> · parks in {park}</span>;
  if (cap) return <span className="text-tertiary"> · runaway cap in {cap}</span>;
  return null;
}

function ResizeButton({ session: s, onResize }: { session: Session; onResize: (jobId: string) => void }) {
  const [open, setOpen] = useState(false);
  const machines = useQuery({
    queryKey: ["machines", s.id],
    queryFn: () => rpc.call("sessions.machines", { id: s.id }),
    enabled: open,
  });
  const resize = async (type: string) => {
    setOpen(false);
    const j = await attempt(() => rpc.call("sessions.resize", { id: s.id, machineType: type }));
    if (j) {
      trackJob(j);
      onResize(j.id);
    }
  };
  return (
    <DialogTrigger isOpen={open} onOpenChange={setOpen}>
      <Button icon={<Cpu size={13} />}>Resize</Button>
      <Popover
        placement="bottom end"
        className="w-80 rounded-card border border-separator bg-surface p-1 shadow-xl outline-none"
      >
        <Dialog className="outline-none" aria-label="Machine types">
          <div className="px-2 pt-1.5 pb-1 text-xs text-secondary">
            {s.state === "parked"
              ? "Resized in place; it stays parked."
              : "Parks, changes type, resumes — about a minute or two."}
          </div>
          {machines.isLoading && (
            <div className="flex justify-center p-3">
              <Spinner />
            </div>
          )}
          {machines.data?.map((m) => (
            <AriaButton
              key={m.type}
              isDisabled={m.type === s.machineType}
              onPress={() => void resize(m.type)}
              className="flex w-full items-center gap-3 rounded-ctl px-2 py-1.5 text-left text-sm outline-none hover:bg-accent-fill hover:text-on-accent disabled:opacity-50 disabled:hover:bg-transparent disabled:hover:text-label"
            >
              <span className="w-28 font-medium">{m.type}</span>
              <span className="flex-1 text-xs opacity-80">
                {m.cpu} vCPU · {m.mem} GiB
              </span>
              <span className="text-xs tabular-nums opacity-80">{m.cost}</span>
            </AriaButton>
          ))}
        </Dialog>
      </Popover>
    </DialogTrigger>
  );
}
