import { useQuery } from "@tanstack/react-query";
import { Anchor, Check, CircleAlert } from "lucide-react";
import { useEffect, useState } from "react";
import { rpc } from "@/api/client";
import type { SetupDraft, SetupResult, SetupState } from "@/api/protocol.gen";
import { Toolbar } from "@/app/toolbar";
import { Button } from "@/ui/Button";
import { Empty } from "@/ui/Empty";
import { Spinner } from "@/ui/Spinner";
import { AccountStep } from "./AccountStep";
import { DefaultsStep } from "./DefaultsStep";
import { DoneStep, FinishStep } from "./FinishStep";
import { CopyCommand, Nav } from "./parts";

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
