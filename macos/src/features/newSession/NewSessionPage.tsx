// New Session: a message box, like a chat's. Its first message is the
// session's first prompt, and the session and its branch are named after
// it; the model and how it runs are the composer's pickers. Where it
// branches from sits to one side, for when it matters.
import { useQuery } from "@tanstack/react-query";
import { useNavigate, useSearch } from "@tanstack/react-router";
import { ArrowUp, ChevronRight, GitBranch, Hammer } from "lucide-react";
import { type KeyboardEvent, useEffect, useRef, useState } from "react";
import { Button as AriaButton, Dialog, DialogTrigger, Popover } from "react-aria-components";
import { rpc } from "@/api/client";
import type { Job } from "@/api/protocol.gen";
import { RepoTile } from "@/app/crumbs";
import { Toolbar } from "@/app/toolbar";
import { RunPickers, type RunSettings, useModels } from "@/features/chat/Composer";
import { ModelPicker } from "@/features/chat/ModelPicker";
import { linkCheckout } from "@/features/repos/link";
import { plural } from "@/lib/format";
import { startJob, trackJob } from "@/state/jobs";
import { useOverview } from "@/state/overview";
import { usePrefs } from "@/state/prefs";
import { useChatDefaults } from "@/state/settings";
import { attempt } from "@/state/toasts";
import { ActionButton, Button, IconButton } from "@/ui/Button";
import { Select } from "@/ui/Select";
import { Spinner } from "@/ui/Spinner";

const chip =
  "inline-flex h-7 max-w-full items-center gap-1.5 rounded-[7px] px-2 text-sm text-secondary outline-none hover:bg-hover hover:text-label focus-visible:ring-2 focus-visible:ring-accent/50";

export function NewSessionPage() {
  const search = useSearch({ from: "/new" });
  const navigate = useNavigate();
  const o = useOverview();
  const checkouts = (o?.checkouts ?? []).filter((c) => !c.missing);
  const [path, setPath] = useState("");
  useEffect(() => {
    if (path && checkouts.some((c) => c.path === path)) return;
    const pick = checkouts.find((c) => c.name === search.repo) ?? checkouts[0];
    if (pick) setPath(pick.path);
  }, [checkouts, search.repo, path]);
  const checkout = checkouts.find((c) => c.path === path);
  const repo = o?.repos.find((r) => r.checkout?.path === path);

  const defaults = useChatDefaults();
  const [run, setRun] = useState<RunSettings & { agent: string }>(() => ({ ...defaults() }));
  const settingsLoaded = useRef(false);
  const d = defaults();
  useEffect(() => {
    // The defaults arrive with the settings: take them once.
    if (settingsLoaded.current || !d.model) return;
    settingsLoaded.current = true;
    setRun({ ...d });
  }, [d]);
  const models = useModels(run.agent);

  const [prompt, setPrompt] = useState(search.prompt ?? "");
  // The branch is a stand-in the first prompt renames (a link can name it).
  const [standIn, setStandIn] = useState("");
  const name = search.branch || standIn;
  const [base, setBase] = useState("");
  useEffect(() => {
    if (search.branch) return;
    void rpc.call("sessions.suggestName", {}).then((r) => {
      // The first name to come back stands (an effect can run twice).
      setStandIn((x) => x || r.name);
    });
  }, [search.branch]);
  const info = useQuery({
    queryKey: ["inspect", path],
    queryFn: () => rpc.call("repos.inspect", { path }),
    enabled: !!path,
    staleTime: 2_000,
  });

  const [job, setJob] = useState<Job>();
  const [busy, setBusy] = useState(false);
  const canStart = !!path && !!name && !busy && !repo?.needsImage;

  const start = async () => {
    if (!canStart) return;
    setBusy(true);
    const text = prompt.trim();
    const j = await attempt(() =>
      rpc.call("sessions.create", {
        path,
        branch: name,
        // Origin's default branch unless told otherwise (pier's own default);
        // "HEAD" is the clone as it is.
        base: base === "" ? undefined : base,
        autoName: (!search.branch && !!text) || undefined,
        chat: {
          agent: run.agent,
          model: run.model || undefined,
          effort: run.effort || undefined,
          fast: run.fast || undefined,
          mode: run.mode || undefined,
          prompt: text || undefined,
        },
      }),
    );
    setBusy(false);
    if (!j) return;
    trackJob(j);
    setJob(j);
  };

  // Once the session lists, go to it.
  const made = job && o?.sessions.find((s) => s.name === job.session && !s.pooled);
  useEffect(() => {
    if (made) navigate({ to: "/session/$id", params: { id: made.id } });
  }, [made, navigate]);

  const sendKey = usePrefs((p) => p.sendKey);
  const onKey = (e: KeyboardEvent<HTMLTextAreaElement>) => {
    if (e.key !== "Enter" || e.nativeEvent.isComposing) return;
    if (sendKey === "enter" ? e.shiftKey || e.metaKey : !e.metaKey) return;
    e.preventDefault();
    void start();
  };

  return (
    <div className="flex h-full flex-col">
      <Toolbar>
        <div data-tauri-drag-region className="flex min-w-0 flex-1 items-center gap-2.5">
          {checkout && <RepoTile name={checkout.name} />}
          <span className="font-medium text-secondary">{checkout?.name ?? "No repository"}</span>
          <ChevronRight size={14} className="text-quaternary" />
          <h1 className="text-md font-semibold">New session</h1>
        </div>
      </Toolbar>
      <div className="mx-auto flex w-full max-w-[760px] flex-1 flex-col justify-center px-8 pb-[12vh]">
        {job ? (
          <div className="flex flex-col items-center gap-3 text-center">
            <Spinner />
            <div className="text-md font-semibold">Starting {job.session}</div>
            <div className="text-sm text-secondary">
              It opens here as soon as it's up — usually a minute or two.
            </div>
          </div>
        ) : !o ? (
          <div className="flex justify-center">
            <Spinner />
          </div>
        ) : checkouts.length === 0 ? (
          <div className="flex flex-col items-center gap-3 text-center">
            <div className="text-md font-semibold">Link a repository first</div>
            <Button variant="primary" onPress={() => void linkCheckout()}>
              Link a repository…
            </Button>
          </div>
        ) : (
          <>
            <h2 className="mb-4 text-center text-xl font-semibold">What should it work on?</h2>
            <div className="rounded-[14px] border border-strong bg-raised shadow-sm focus-within:border-accent/60">
              <textarea
                autoFocus
                rows={3}
                value={prompt}
                onChange={(e) => setPrompt(e.target.value)}
                onKeyDown={onKey}
                aria-label="First prompt"
                placeholder="Describe the task — the session starts on it once it's set up. Optional."
                className="selectable block max-h-[40vh] min-h-[84px] w-full resize-none bg-transparent px-4 pt-3.5 pb-1.5 text-[14px] leading-[1.5] outline-none placeholder:text-tertiary"
              />
              <div className="flex items-center gap-0.5 px-2 pb-2">
                <ModelPicker
                  value={{ agent: run.agent, model: run.model, effort: run.effort, fast: !!run.fast }}
                  onChange={(p) =>
                    setRun((r) =>
                      p.agent && p.agent !== r.agent
                        ? { ...defaults(p.agent), model: p.model ?? "", fast: r.fast }
                        : { ...r, ...p },
                    )
                  }
                />
                <RunPickers
                  models={models.data}
                  value={run}
                  onChange={(p) => setRun((r) => ({ ...r, ...p }))}
                  withModel={false}
                  withEffort={false}
                />
                <span className="flex-1" />
                <IconButton
                  label="Start the session"
                  isDisabled={!canStart}
                  onPress={() => void start()}
                  className="rounded-full bg-accent-fill text-on-accent hover:bg-accent-fill hover:text-on-accent hover:brightness-110 disabled:bg-selected disabled:text-tertiary"
                >
                  <ArrowUp size={15} strokeWidth={2.4} />
                </IconButton>
              </div>
            </div>
            <div className="mt-2 flex flex-wrap items-center gap-1 px-1">
              {checkouts.length > 1 && (
                <Select
                  label="Repository"
                  variant="ghost"
                  size="sm"
                  placement="bottom start"
                  value={path}
                  onChange={setPath}
                  options={checkouts.map((c) => ({ value: c.path, label: c.name }))}
                />
              )}
              <BaseChip
                value={base}
                onChange={setBase}
                defaultBranch={info.data?.defaultBranch ?? ""}
                clone={info.data?.branch || "detached HEAD"}
                dirty={info.data?.dirty ?? 0}
              />
            </div>
            {repo?.needsImage && (
              <div className="mt-4 flex items-center gap-3 rounded-ctl bg-warn/10 px-3.5 py-3 text-sm">
                <span className="min-w-0 flex-1">
                  {repo.baking
                    ? `${repo.name}'s session image is baking — sessions can start once it's done.`
                    : `${repo.name} needs its session image first.`}
                </span>
                {!repo.baking && (
                  <ActionButton
                    size="sm"
                    icon={<Hammer size={12} />}
                    action={() => startJob(() => rpc.call("image.bake", { path }))}
                  >
                    Bake now
                  </ActionButton>
                )}
              </div>
            )}
          </>
        )}
      </div>
    </div>
  );
}

/**
 * Where the session branches from: origin's default branch unless told
 * otherwise ("" — and its pull request targets it), the clone as it is
 * ("HEAD": its branch, uncommitted changes and all), or a ref as git
 * names it: origin/<branch> is fetched first; anything else is this
 * Mac's clone as committed.
 */
function BaseChip({
  value,
  onChange,
  defaultBranch,
  clone,
  dirty,
}: {
  value: string;
  onChange: (v: string) => void;
  defaultBranch: string;
  clone: string;
  dirty: number;
}) {
  const label =
    value === ""
      ? defaultBranch
        ? `origin/${defaultBranch}`
        : clone
      : value === "HEAD"
        ? `${clone} (local)`
        : value;
  const typed = value === "" || value === "HEAD" ? "" : value;
  const option = (v: string, title: string, sub: string) => (
    <button
      type="button"
      onClick={() => onChange(v)}
      className={`flex w-full flex-col items-start rounded-[6px] px-2 py-1.5 text-left hover:bg-hover ${value === v ? "bg-selected" : ""}`}
    >
      <span className="text-base">{title}</span>
      <span className="text-xs text-tertiary">{sub}</span>
    </button>
  );
  return (
    <DialogTrigger>
      <AriaButton className={chip}>
        <GitBranch size={13} className="text-tertiary" />
        <span className="truncate font-mono">{label}</span>
      </AriaButton>
      <Popover
        placement="bottom start"
        className="pop-in w-80 rounded-card bg-popover p-1.5 shadow-(--shadow-pop)"
      >
        <Dialog aria-label="Branch from" className="flex flex-col gap-0.5 outline-none">
          {defaultBranch &&
            option("", `origin/${defaultBranch}`, `Fetched fresh; pull requests target ${defaultBranch}.`)}
          {option(
            "HEAD",
            `${clone} (local)`,
            `This Mac's clone as it is${dirty > 0 ? `, with ${plural(dirty, "uncommitted change")}` : ""}.`,
          )}
          <div className="mx-2 mt-1.5 mb-1 h-px bg-separator" />
          <input
            aria-label="Another branch or commit"
            value={typed}
            onChange={(e) => onChange(e.target.value.trim())}
            placeholder="origin/branch, a local branch or a commit"
            className="mx-1 mb-1 h-8 rounded-ctl border border-separator bg-control px-2 font-mono text-sm outline-none focus:border-accent"
          />
          {typed && (
            <span className="mx-1.5 mb-1 text-xs text-tertiary">
              {typed.startsWith("origin/")
                ? "Fetched fresh from origin."
                : "From this Mac's clone, as committed."}
            </span>
          )}
        </Dialog>
      </Popover>
    </DialogTrigger>
  );
}
