// Setup's last steps: laying the groundwork, then what came of it.
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { Check, CircleAlert } from "lucide-react";
import { useState } from "react";
import { rpc } from "@/api/client";
import type { SetupDraft, SetupResult } from "@/api/protocol.gen";
import { RpcError } from "@/api/rpc";
import { linkCheckout } from "@/features/repos/link";
import { attempt, toast } from "@/state/toasts";
import { Button } from "@/ui/Button";
import { Field } from "@/ui/Field";
import { LogView } from "@/ui/LogView";
import { CopyCommand, Nav } from "./parts";

export function FinishStep({
  draft,
  update,
  skills,
  back,
  done,
}: {
  draft: SetupDraft;
  update: (p: Partial<SetupDraft>) => void;
  skills: string[];
  back: () => void;
  done: (r: SetupResult) => void;
}) {
  const qc = useQueryClient();
  const [busy, setBusy] = useState(false);
  const [problem, setProblem] = useState<{ kind: string; message: string }>();
  const [adminDoc, setAdminDoc] = useState("");
  const apply = async () => {
    setBusy(true);
    setProblem(undefined);
    try {
      const r = await rpc.call("setup.apply", { draft, skills });
      void qc.invalidateQueries();
      done(r);
    } catch (e) {
      const kind = e instanceof RpcError ? e.kind : "failed";
      setProblem({ kind, message: (e as Error).message });
      if (kind === "no_permission") {
        const doc = await rpc.call("setup.adminDoc", { cloud: draft.cloud }).catch(() => undefined);
        setAdminDoc(doc?.text ?? "");
      }
    } finally {
      setBusy(false);
    }
  };
  const groundwork =
    draft.cloud === "gcp-gce"
      ? "the compute and IAP APIs, and firewall rules that let only IAP reach sessions"
      : "an IAM role and instance profile, and a security group with no way in";
  return (
    <section className="flex flex-col gap-4">
      <div className="text-sm">
        pier now creates what the account needs once — {groundwork} — finding whatever is there already, then
        saves your answers and checks everything.
      </div>
      {problem && (
        <div className="rounded-card border border-bad/40 bg-bad/8 p-3 text-sm">
          <div className="selectable">{problem.message}</div>
          {problem.kind === "no_default_vpc" && (
            <div className="mt-2">
              <Field
                label="Subnet for sessions"
                value={draft.awsSubnet ?? ""}
                onChange={(v) => update({ awsSubnet: v })}
                placeholder="subnet-…"
                mono
                hint="The account has no default VPC: pick a subnet with internet access."
              />
            </div>
          )}
          {problem.kind === "no_billing" && (
            <div className="mt-1 text-secondary">Link a billing account to the project, then try again.</div>
          )}
          {problem.kind === "no_permission" && adminDoc && (
            <div className="mt-2">
              <div className="mb-1 text-secondary">Your login can't create these. An admin can, with:</div>
              <LogView text={adminDoc} className="max-h-64" />
            </div>
          )}
        </div>
      )}
      <Nav
        back={back}
        next={() => void apply()}
        nextLabel={busy ? "Setting up…" : problem ? "Try again" : "Set up"}
        canNext={!busy}
      />
    </section>
  );
}

export function DoneStep({ result }: { result: SetupResult }) {
  const navigate = useNavigate();
  const cli = useQuery({ queryKey: ["cli"], queryFn: () => rpc.call("cli.status", {}) });
  const failed = result.checks.filter((c) => c.status !== "ok");
  return (
    <section className="flex flex-col gap-4">
      <div className="flex items-center gap-2 text-lg font-semibold">
        <Check size={20} className="text-ok" /> pier is ready
      </div>
      <div className="rounded-card border border-separator text-sm">
        {result.created.map((c) => (
          <div key={c} className="border-t border-separator px-3 py-1.5 first:border-t-0">
            <span className="text-ok">+</span> created {c}
          </div>
        ))}
        {result.existed.map((c) => (
          <div key={c} className="border-t border-separator px-3 py-1.5 text-secondary first:border-t-0">
            = found {c}
          </div>
        ))}
        {result.checks.map((c) => (
          <div
            key={c.name}
            className="flex items-center gap-2 border-t border-separator px-3 py-1.5 first:border-t-0"
          >
            {c.status === "ok" ? (
              <Check size={13} className="text-ok" />
            ) : (
              <CircleAlert size={13} className="text-warn" />
            )}
            <span>{c.name}</span>
            <span className="min-w-0 flex-1 truncate text-xs text-secondary">{c.detail}</span>
            {c.fix && <CopyCommand text={c.fix} />}
          </div>
        ))}
      </div>
      {failed.length > 0 && (
        <div className="text-sm text-secondary">Sessions work without these; fix them when it suits.</div>
      )}
      <div className="flex flex-wrap gap-2">
        <Button variant="primary" onPress={async () => (await linkCheckout()) && navigate({ to: "/" })}>
          Link a repository…
        </Button>
        {cli.data && !cli.data.installed && !cli.data.provided && (
          <Button
            onPress={async () => {
              const r = await attempt(() => rpc.call("cli.install", {}));
              if (r?.installed) toast("The pier command is installed", "ok");
              void cli.refetch();
            }}
          >
            Install the pier command
          </Button>
        )}
        <Button onPress={() => navigate({ to: "/" })}>Done</Button>
      </div>
    </section>
  );
}
