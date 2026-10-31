// Setup's account step: the cloud login (an MFA code when asked), and the
// region or project and zone.
import { useQuery } from "@tanstack/react-query";
import { Check } from "lucide-react";
import { useState } from "react";
import { rpc } from "@/api/client";
import type { SetupDraft, SetupState } from "@/api/protocol.gen";
import { RpcError } from "@/api/rpc";
import { attempt } from "@/state/toasts";
import { Button } from "@/ui/Button";
import { Field } from "@/ui/Field";
import { Nav } from "./parts";

export function AccountStep({
  st,
  draft,
  update,
  identity,
  setIdentity,
  back,
  next,
}: {
  st: SetupState;
  draft: SetupDraft;
  update: (p: Partial<SetupDraft>) => void;
  identity: string;
  setIdentity: (s: string) => void;
  back: () => void;
  next: () => void;
}) {
  const aws = draft.cloud === "aws-ec2";
  const [checking, setChecking] = useState(false);
  const [error, setError] = useState<{ message: string; login: boolean }>();
  const [lines, setLines] = useState<string[]>([]);
  const [signingIn, setSigningIn] = useState(false);
  // A profile that asks for an MFA code: which device, and the code.
  const [mfa, setMfa] = useState<string>();
  const [code, setCode] = useState("");
  const regions = useQuery({
    queryKey: ["setup-regions", draft.cloud, draft.awsProfile, draft.gcpProject, !!identity],
    queryFn: () =>
      rpc.call("setup.regions", {
        cloud: draft.cloud,
        profile: draft.awsProfile || "default",
        project: draft.gcpProject,
      }),
    staleTime: 300_000,
  });
  const check = async (mfaCode?: string) => {
    setChecking(true);
    setError(undefined);
    try {
      const r = await rpc.call("setup.check", {
        cloud: draft.cloud,
        profile: draft.awsProfile || "default",
        project: draft.gcpProject,
        mfaCode,
      });
      setIdentity(r.identity);
      setMfa(undefined);
      setCode("");
      if (aws && !draft.awsRegion && r.region) update({ awsRegion: r.region });
    } catch (e) {
      if (e instanceof RpcError && e.kind === "mfa_required") {
        setMfa((e as Error).message);
        return;
      }
      setError({ message: (e as Error).message, login: e instanceof RpcError && e.kind === "auth_expired" });
    } finally {
      setChecking(false);
    }
  };
  const signIn = async () => {
    setSigningIn(true);
    setLines([]);
    await attempt(() =>
      rpc.call(
        "setup.login",
        { cloud: draft.cloud, profile: draft.awsProfile || "default" },
        { onProgress: (e) => setLines((l) => [...l.slice(-5), e.message]) },
      ),
    );
    setSigningIn(false);
    await check();
  };
  return (
    <section className="flex flex-col gap-4">
      {aws ? (
        <>
          <div className="flex flex-col gap-1">
            <label htmlFor="aws-profile" className="text-xs font-medium text-secondary">
              AWS profile
            </label>
            <input
              id="aws-profile"
              list="aws-profiles"
              value={draft.awsProfile ?? ""}
              placeholder="default"
              onChange={(e) => update({ awsProfile: e.target.value })}
              className="selectable h-7 rounded-ctl border border-separator bg-control px-2 text-base outline-none focus:border-accent"
            />
            <datalist id="aws-profiles">
              {st.awsProfiles.map((p) => (
                <option key={p} value={p} />
              ))}
            </datalist>
          </div>
          <div className="flex flex-col gap-1">
            <label htmlFor="aws-region" className="text-xs font-medium text-secondary">
              Region
            </label>
            <input
              id="aws-region"
              list="aws-regions"
              value={draft.awsRegion ?? ""}
              placeholder="eu-central-1"
              onChange={(e) => update({ awsRegion: e.target.value })}
              className="selectable h-7 rounded-ctl border border-separator bg-control px-2 font-mono text-base outline-none focus:border-accent"
            />
            <datalist id="aws-regions">
              {(regions.data?.regions ?? []).map((r) => (
                <option key={r} value={r} />
              ))}
            </datalist>
          </div>
        </>
      ) : (
        <>
          <div className="text-sm text-secondary">
            {st.gcpAccount ? `gcloud is signed in as ${st.gcpAccount}.` : "gcloud isn't signed in yet."}
          </div>
          <Field
            label="Project"
            value={draft.gcpProject ?? ""}
            onChange={(v) => update({ gcpProject: v })}
            placeholder="my-project"
            mono
            hint="Sessions, their disks and images live in this project."
          />
          <div className="flex flex-col gap-1">
            <label htmlFor="gcp-zone" className="text-xs font-medium text-secondary">
              Zone
            </label>
            <input
              id="gcp-zone"
              list="gcp-zones"
              value={draft.gcpZone ?? ""}
              placeholder="europe-west4-a"
              onChange={(e) => update({ gcpZone: e.target.value })}
              className="selectable h-7 rounded-ctl border border-separator bg-control px-2 font-mono text-base outline-none focus:border-accent"
            />
            <datalist id="gcp-zones">
              {(regions.data?.regions ?? []).map((z) => (
                <option key={z} value={z} />
              ))}
            </datalist>
          </div>
        </>
      )}
      <div className="flex items-center gap-2">
        <Button onPress={() => void check()} isDisabled={checking || signingIn}>
          {checking ? "Checking…" : "Check the sign-in"}
        </Button>
        {identity && (
          <span className="flex min-w-0 items-center gap-1 text-sm">
            <Check size={14} className="shrink-0 text-ok" />
            <span className="selectable truncate">{identity}</span>
          </span>
        )}
      </div>
      {mfa && (
        <form
          className="flex flex-col gap-2 rounded-card border border-separator bg-raised p-3 text-sm"
          onSubmit={(e) => {
            e.preventDefault();
            if (code.trim()) void check(code);
          }}
        >
          <div className="selectable text-secondary">{mfa}.</div>
          <div className="flex items-center gap-2">
            <input
              aria-label="MFA code"
              autoFocus
              inputMode="numeric"
              autoComplete="one-time-code"
              value={code}
              onChange={(e) => setCode(e.target.value.replace(/\D/g, "").slice(0, 8))}
              placeholder="123456"
              className="selectable h-7 w-32 rounded-ctl border border-separator bg-control px-2 font-mono text-base tracking-widest outline-none focus:border-accent"
            />
            <Button type="submit" size="sm" variant="primary" isDisabled={checking || code.length < 6}>
              {checking ? "Checking…" : "Use code"}
            </Button>
          </div>
        </form>
      )}
      {error && (
        <div className="rounded-card border border-warn/40 bg-warn/8 p-3 text-sm">
          <div className="selectable">{error.message}</div>
          {(error.login || !aws) && (
            <div className="mt-2 flex items-center gap-2">
              <Button size="sm" variant="primary" isDisabled={signingIn} onPress={() => void signIn()}>
                {signingIn ? "Waiting for the browser…" : "Sign in"}
              </Button>
            </div>
          )}
          {lines.map((l) => (
            <div key={l} className="selectable mt-1 font-mono text-xs text-secondary">
              {l}
            </div>
          ))}
        </div>
      )}
      <Nav back={back} next={next} canNext={!!identity} />
    </section>
  );
}
