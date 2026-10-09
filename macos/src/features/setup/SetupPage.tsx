import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { Anchor, Check, CircleAlert, Copy, Terminal } from "lucide-react";
import { useCallback, useEffect, useState } from "react";
import { rpc } from "@/api/client";
import type { LocalPurpose, SetupDraft, SetupResult, SetupState } from "@/api/protocol.gen";
import { RpcError } from "@/api/rpc";
import { Toolbar } from "@/app/toolbar";
import { linkCheckout } from "@/features/repos/link";
import { attempt, toast } from "@/state/toasts";
import { Button } from "@/ui/Button";
import { Empty } from "@/ui/Empty";
import { Field } from "@/ui/Field";
import { LogView } from "@/ui/LogView";
import { Spinner } from "@/ui/Spinner";
import { LocalTerminalSheet } from "./LocalTerminalSheet";

type Step = "cloud" | "account" | "defaults" | "finish" | "done";
const steps: [Step, string][] = [
  ["cloud", "Cloud"],
  ["account", "Account"],
  ["defaults", "Defaults"],
  ["finish", "Finish"],
];

const clouds = [
  { id: "aws-ec2", name: "AWS", detail: "EC2, reached over SSM or ssh" },
  { id: "gcp-gce", name: "Google Cloud", detail: "Compute Engine, reached over IAP" },
] as const;

/**
 * Setting pier up: which cloud and account sessions run in, the defaults
 * new sessions get (with what they cost), and the account's one-time
 * groundwork. Everything found on this Mac is filled in already.
 */
export function SetupPage() {
  const detect = useQuery({ queryKey: ["setup"], queryFn: () => rpc.call("setup.detect", {}), staleTime: 0 });
  const [step, setStep] = useState<Step>("cloud");
  const [draft, setDraft] = useState<SetupDraft>();
  const [identity, setIdentity] = useState("");
  const [skills, setSkills] = useState<string[]>([]);
  const [result, setResult] = useState<SetupResult>();
  useEffect(() => {
    if (detect.data && !draft) {
      setDraft(detect.data.draft);
      setSkills(detect.data.agents);
    }
  }, [detect.data, draft]);

  if (detect.error) return <Empty title="Setup" detail={(detect.error as Error).message} />;
  if (!detect.data || !draft) {
    return (
      <div className="flex h-full items-center justify-center">
        <Spinner />
      </div>
    );
  }
  const st = detect.data;
  const update = (p: Partial<SetupDraft>) => {
    setDraft({ ...draft, ...p });
    if ("cloud" in p || "awsProfile" in p || "gcpProject" in p) setIdentity("");
  };
  const at = steps.findIndex(([s]) => s === step);
  return (
    <div className="h-full overflow-y-auto">
      <Toolbar>
        <h1 data-tauri-drag-region className="flex-1 text-base font-semibold">
          Setup
        </h1>
      </Toolbar>
      <div className="mx-auto flex max-w-2xl flex-col gap-5 p-8">
        <header>
          <div className="flex items-center gap-2 text-xl font-semibold">
            <Anchor size={20} className="text-accent" /> Set up pier
          </div>
          <div className="mt-1 text-sm text-secondary">
            Sessions run in your own cloud account; nothing leaves it.
            {st.configured && " Your current answers are filled in."}
          </div>
          {step !== "done" && (
            <ol className="mt-4 flex gap-1 text-xs">
              {steps.map(([s, title], i) => (
                <li
                  key={s}
                  className={`flex-1 border-t-2 pt-1.5 ${i <= at ? "border-accent text-label" : "border-separator text-tertiary"}`}
                >
                  {i + 1} · {title}
                </li>
              ))}
            </ol>
          )}
        </header>
        {step === "cloud" && (
          <CloudStep
            st={st}
            draft={draft}
            update={update}
            onRecheck={() => void detect.refetch()}
            next={() => setStep("account")}
          />
        )}
        {step === "account" && (
          <AccountStep
            st={st}
            draft={draft}
            update={update}
            identity={identity}
            setIdentity={setIdentity}
            back={() => setStep("cloud")}
            next={() => setStep("defaults")}
          />
        )}
        {step === "defaults" && (
          <DefaultsStep
            st={st}
            draft={draft}
            update={update}
            skills={skills}
            setSkills={setSkills}
            back={() => setStep("account")}
            next={() => setStep("finish")}
          />
        )}
        {step === "finish" && (
          <FinishStep
            draft={draft}
            update={update}
            skills={skills}
            back={() => setStep("defaults")}
            done={(r) => {
              setResult(r);
              setStep("done");
            }}
          />
        )}
        {step === "done" && result && <DoneStep result={result} />}
      </div>
    </div>
  );
}

function Nav({
  back,
  next,
  nextLabel = "Continue",
  canNext = true,
}: {
  back?: () => void;
  next?: () => void;
  nextLabel?: string;
  canNext?: boolean;
}) {
  return (
    <div className="mt-2 flex justify-between">
      {back ? <Button onPress={back}>Back</Button> : <span />}
      {next && (
        <Button variant="primary" isDisabled={!canNext} onPress={next}>
          {nextLabel}
        </Button>
      )}
    </div>
  );
}

function CopyCommand({ text }: { text: string }) {
  return (
    <span className="inline-flex items-center gap-1">
      <code className="selectable rounded bg-selected px-1.5 py-0.5 font-mono text-[11.5px]">{text}</code>
      <button
        type="button"
        aria-label="Copy"
        onClick={() => {
          void navigator.clipboard.writeText(text);
          toast("Copied", "ok");
        }}
        className="text-tertiary hover:text-label"
      >
        <Copy size={11} />
      </button>
    </span>
  );
}

function CloudStep({
  st,
  draft,
  update,
  onRecheck,
  next,
}: {
  st: SetupState;
  draft: SetupDraft;
  update: (p: Partial<SetupDraft>) => void;
  onRecheck: () => void;
  next: () => void;
}) {
  const tools = draft.cloud === "gcp-gce" ? st.gcpTools : st.awsTools;
  const missing = tools.filter((t) => !t.found);
  return (
    <section className="flex flex-col gap-4">
      <div className="grid grid-cols-2 gap-3">
        {clouds.map((c) => (
          <button
            key={c.id}
            type="button"
            aria-pressed={draft.cloud === c.id}
            onClick={() => update({ cloud: c.id })}
            className={`rounded-card border p-4 text-left ${draft.cloud === c.id ? "border-accent bg-accent-soft" : "border-separator hover:bg-hover"}`}
          >
            <div className="text-md font-semibold">{c.name}</div>
            <div className="mt-0.5 text-xs text-secondary">{c.detail}</div>
          </button>
        ))}
      </div>
      <div className="rounded-card border border-separator">
        {tools.map((t) => (
          <div
            key={t.name}
            className="flex items-center gap-2 border-t border-separator px-3 py-2 text-sm first:border-t-0"
          >
            {t.found ? (
              <Check size={14} className="text-ok" />
            ) : (
              <CircleAlert size={14} className="text-warn" />
            )}
            <span className="font-mono">{t.name}</span>
            <span className="flex-1" />
            {!t.found &&
              (t.hint.startsWith("brew ") ? (
                <CopyCommand text={t.hint} />
              ) : (
                <span className="text-xs text-secondary">{t.hint}</span>
              ))}
          </div>
        ))}
      </div>
      {missing.length > 0 && (
        <div className="flex items-center gap-2 text-sm text-secondary">
          Install what's missing, then
          <Button size="sm" onPress={onRecheck}>
            Check again
          </Button>
        </div>
      )}
      <Nav next={next} canNext={missing.length === 0} />
    </section>
  );
}

function AccountStep({
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

const disks = [20, 40, 80, 160];
const idles = ["15m", "30m", "1h", "2h", "never"];
const caps = ["4h", "8h", "24h", "never"];
const speeds: [string, string][] = [
  ["fast", "Fast"],
  ["lean", "Lean"],
  ["minimal", "Minimal"],
];

function DefaultsStep({
  st,
  draft,
  update,
  skills,
  setSkills,
  back,
  next,
}: {
  st: SetupState;
  draft: SetupDraft;
  update: (p: Partial<SetupDraft>) => void;
  skills: string[];
  setSkills: (s: string[]) => void;
  back: () => void;
  next: () => void;
}) {
  const defaults = useQuery({
    queryKey: ["setup-defaults", draft],
    queryFn: () => rpc.call("setup.defaults", draft),
    placeholderData: (prev) => prev,
  });
  const [terminal, setTerminal] = useState<LocalPurpose>();
  const qc = useQueryClient();
  // The token went into pier's settings: the field says it's saved.
  const onTokenCaptured = useCallback(() => void qc.invalidateQueries({ queryKey: ["setup"] }), [qc]);
  const note = (key: string) => defaults.data?.rows.find((r) => r.key === key)?.note;
  const select = (
    label: string,
    value: string,
    options: string[],
    onChange: (v: string) => void,
    render = (o: string) => o,
  ) => (
    <select
      aria-label={label}
      value={value}
      onChange={(e) => onChange(e.target.value)}
      className="h-7 w-auto min-w-44 max-w-full rounded-ctl border border-separator bg-control px-2 text-base outline-none"
    >
      {options.map((o) => (
        <option key={o} value={o}>
          {render(o)}
        </option>
      ))}
    </select>
  );
  const machines = defaults.data?.machines ?? [];
  const row = (label: string, control: React.ReactNode, hint?: string) => (
    <div className="flex items-start gap-4 border-t border-separator px-3 py-2.5 first:border-t-0">
      <div className="w-28 shrink-0 pt-1 text-sm text-secondary">{label}</div>
      <div className="min-w-0 flex-1">
        {control}
        {hint && <div className="mt-1 text-xs text-secondary">{hint}</div>}
      </div>
    </div>
  );
  return (
    <section className="flex flex-col gap-4">
      <div className="text-sm text-secondary">
        What every new session gets. All of it changes later in Settings.
      </div>
      <div className="rounded-card border border-separator bg-raised/40">
        {row(
          "Machine",
          select(
            "Machine",
            draft.machine,
            machines.length ? machines.map((m) => m.type) : [draft.machine],
            (v) => update({ machine: v }),
            (t) => {
              const m = machines.find((x) => x.type === t);
              return m ? `${t} — ${m.cpu} vCPU · ${m.mem} GB · ${m.cost}` : t;
            },
          ),
          "Resize any session later.",
        )}
        {row(
          "Disk",
          select(
            "Disk",
            String(draft.diskGib),
            disks.map(String),
            (v) => update({ diskGib: Number(v) }),
            (d) => `${d} GiB`,
          ),
          note(draft.cloud === "gcp-gce" ? "gcp.disk_gib" : "aws.disk_gib"),
        )}
        {row(
          "Park after",
          select(
            "Park after",
            draft.idleTimeout,
            idles,
            (v) => update({ idleTimeout: v }),
            (v) => (v === "never" ? "never" : `${v} idle`),
          ),
          note("idle_timeout"),
        )}
        {row(
          "Runaway cap",
          select("Runaway cap", draft.unattendedCap, caps, (v) => update({ unattendedCap: v })),
          note("unattended_cap"),
        )}
        {row(
          "Speed",
          <fieldset
            aria-label="Speed"
            className="m-0 flex w-fit rounded-ctl border-0 bg-selected p-0.5 text-sm"
          >
            {speeds.map(([v, label]) => (
              <button
                key={v}
                type="button"
                aria-pressed={draft.speed === v}
                onClick={() => update({ speed: v })}
                className={`rounded-[4px] px-3 py-0.5 ${draft.speed === v ? "bg-control text-label shadow-sm" : "text-secondary"}`}
              >
                {label}
              </button>
            ))}
          </fieldset>,
          note("speed"),
        )}
        {row(
          "Copied in",
          draft.manifest.length === 0 ? (
            <span className="text-sm text-secondary">No agent config found on this Mac.</span>
          ) : (
            <div className="flex flex-col gap-1">
              {(st.draft.manifest.length ? st.draft.manifest : draft.manifest).map((m) => (
                <label key={m} className="flex items-center gap-2 text-sm">
                  <input
                    type="checkbox"
                    checked={draft.manifest.includes(m)}
                    onChange={(e) =>
                      update({
                        manifest: e.target.checked
                          ? [...draft.manifest, m]
                          : draft.manifest.filter((x) => x !== m),
                      })
                    }
                    className="accent-[var(--accent)]"
                  />
                  <span className="font-mono text-[12px]">~/{m}</span>
                </label>
              ))}
            </div>
          ),
          "Your agents' config, copied into every session so they start as you have them.",
        )}
        {st.agents.length > 0 &&
          row(
            "Agent skill",
            <div className="flex flex-col gap-1">
              {st.agents.map((a) => (
                <label key={a} className="flex items-center gap-2 text-sm">
                  <input
                    type="checkbox"
                    checked={skills.includes(a)}
                    onChange={(e) =>
                      setSkills(e.target.checked ? [...skills, a] : skills.filter((x) => x !== a))
                    }
                    className="accent-[var(--accent)]"
                  />
                  Install for {a.replace(/^\./, "")}
                </label>
              ))}
            </div>,
            "pier-onboard teaches your agent to set a repository up for pier.",
          )}
        {st.needsClaudeToken &&
          row(
            "Claude token",
            <div className="flex items-center gap-2">
              <input
                type="password"
                aria-label="Claude token"
                value={draft.claudeToken ?? ""}
                onChange={(e) => update({ claudeToken: e.target.value })}
                placeholder={st.hasClaudeToken ? "saved — paste to replace" : "paste the token"}
                className="selectable h-7 w-64 rounded-ctl border border-separator bg-control px-2 font-mono text-sm outline-none focus:border-accent"
              />
              <Button size="sm" icon={<Terminal size={11} />} onPress={() => setTerminal("claude-token")}>
                Get one…
              </Button>
            </div>,
            "Claude's login lives in this Mac's Keychain and can't travel; sessions sign in with a token instead.",
          )}
      </div>
      <Nav back={back} next={next} />
      <LocalTerminalSheet
        purpose={terminal}
        title="Get a Claude token"
        hint="Follow the steps; pier picks up the token it prints."
        onClose={() => setTerminal(undefined)}
        onCaptured={onTokenCaptured}
      />
    </section>
  );
}

function FinishStep({
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

function DoneStep({ result }: { result: SetupResult }) {
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
