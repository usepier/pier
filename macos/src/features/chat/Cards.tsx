import { ClipboardList, MessageCircleQuestion, ShieldAlert } from "lucide-react";
import { useEffect, useState } from "react";
import { rpc } from "@/api/client";
import type { PermissionRequest, Question } from "@/api/protocol.gen";
import { attempt } from "@/state/toasts";
import { Button } from "@/ui/Button";
import { Kbd } from "@/ui/Kbd";
import { answerOf, collect, emptyState, type QuestionState } from "./answers";
import { Markdown } from "./Markdown";
import { Hunks, shortPath } from "./ToolView";

type Decision = {
  behavior: "allow" | "allowSession" | "deny";
  message?: string;
  answers?: Record<string, string>;
  notes?: Record<string, string>;
  mode?: string;
};
type Decide = (d: Decision) => Promise<void>;

/** What a chat's agent waits on, pinned above the composer until answered. */
export function PendingCard({
  sessionId,
  chatId,
  agent,
  agentName,
  req,
}: {
  sessionId: string;
  chatId: string;
  agent: string;
  agentName: string;
  req: PermissionRequest;
}) {
  const [busy, setBusy] = useState(false);
  const decide: Decide = async (d) => {
    setBusy(true);
    await attempt(() => rpc.call("chat.decide", { sessionId, chatId, requestId: req.id, ...d }));
    setBusy(false);
  };
  switch (req.kind) {
    case "question":
      return <QuestionCard key={req.id} req={req} agentName={agentName} busy={busy} decide={decide} />;
    case "plan":
      return (
        <PlanCard key={req.id} req={req} agent={agent} agentName={agentName} busy={busy} decide={decide} />
      );
  }
  return <PermissionCard key={req.id} req={req} agentName={agentName} busy={busy} decide={decide} />;
}

const card =
  "pop-in mx-auto mb-3 w-full max-w-3xl rounded-[12px] border bg-popover shadow-[0_8px_24px_-12px_rgb(0_0_0/0.35)]";

/** What the agent wants to do, in a few words. */
function ask(req: PermissionRequest, agentName: string): string {
  const v = req.call?.view;
  if (v === "bash") return `${agentName} wants to run a command`;
  if (v === "edit") return `${agentName} wants to edit ${shortPath(req.call?.edit?.path || req.summary)}`;
  if (v === "write") return `${agentName} wants to create ${shortPath(req.call?.edit?.path || req.summary)}`;
  if (v === "web") return `${agentName} wants to look something up online`;
  return `${agentName} wants to use ${req.tool}`;
}

function PermissionCard({
  req,
  agentName,
  busy,
  decide,
}: {
  req: PermissionRequest;
  agentName: string;
  busy: boolean;
  decide: Decide;
}) {
  const [why, setWhy] = useState<string | null>(null);
  const call = req.call;
  // Return allows; Escape (with a reason open) goes back.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const typing = (e.target as HTMLElement | null)?.closest("textarea, input");
      if (e.key === "Enter" && (e.metaKey || e.ctrlKey) && !typing && why === null) {
        e.preventDefault();
        void decide({ behavior: "allow" });
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [decide, why]);
  return (
    <div className={`${card} border-warn/40`} role="alertdialog" aria-label={ask(req, agentName)}>
      <div className="flex items-start gap-2.5 px-4 pt-3.5">
        <ShieldAlert size={16} className="mt-0.5 shrink-0 text-warn" />
        <div className="min-w-0 flex-1">
          <div className="text-base font-medium">{ask(req, agentName)}</div>
          {req.reason && <div className="mt-0.5 text-sm text-secondary">{req.reason}</div>}
        </div>
      </div>
      <div className="mt-2.5 max-h-[38vh] overflow-auto px-4">
        {call?.view === "bash" && call.bash ? (
          <pre className="selectable rounded-ctl border border-separator bg-raised px-3 py-2 font-mono text-[12.5px] leading-[1.5] whitespace-pre-wrap break-all">
            <span className="text-tertiary select-none">$ </span>
            {call.bash.command}
          </pre>
        ) : (call?.view === "edit" || call?.view === "write") && call.edit ? (
          <Hunks edit={call.edit} />
        ) : (
          <div className="selectable rounded-ctl border border-separator bg-raised px-3 py-2 font-mono text-[12.5px] break-all">
            {req.summary || req.tool}
          </div>
        )}
      </div>
      {why !== null && (
        <div className="px-4 pt-2.5">
          <textarea
            autoFocus
            value={why}
            rows={2}
            onChange={(e) => setWhy(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter" && !e.shiftKey) {
                e.preventDefault();
                void decide({ behavior: "deny", message: why.trim() || undefined });
              }
              if (e.key === "Escape") setWhy(null);
            }}
            placeholder="Tell it what to do instead (optional)"
            aria-label="Why not"
            className="selectable w-full resize-none rounded-ctl border border-strong bg-control px-2.5 py-1.5 text-base outline-none placeholder:text-tertiary focus:border-accent"
          />
        </div>
      )}
      <div className="flex items-center gap-2 px-4 pt-3 pb-3.5">
        <span className="hidden items-center gap-1 text-xs text-tertiary sm:flex">
          <Kbd>⌘</Kbd>
          <Kbd>⏎</Kbd> allows
        </span>
        <div className="flex-1" />
        {why === null ? (
          <Button size="sm" isDisabled={busy} onPress={() => setWhy("")}>
            Deny…
          </Button>
        ) : (
          <Button
            size="sm"
            isDisabled={busy}
            onPress={() => void decide({ behavior: "deny", message: why.trim() || undefined })}
          >
            Deny
          </Button>
        )}
        {req.allowSession && (
          <Button size="sm" isDisabled={busy} onPress={() => void decide({ behavior: "allowSession" })}>
            Allow for this chat
          </Button>
        )}
        <Button
          size="sm"
          variant="primary"
          isDisabled={busy}
          onPress={() => void decide({ behavior: "allow" })}
        >
          Allow
        </Button>
      </div>
    </div>
  );
}

function QuestionCard({
  req,
  agentName,
  busy,
  decide,
}: {
  req: PermissionRequest;
  agentName: string;
  busy: boolean;
  decide: Decide;
}) {
  const questions = req.questions ?? [];
  const [states, setStates] = useState<QuestionState[]>(() => questions.map(emptyState));
  const set = (i: number, p: Partial<QuestionState>) =>
    setStates((s) => s.map((x, j) => (j === i ? { ...x, ...p } : x)));
  const answered = questions.filter((q, i) => states[i] && answerOf(q, states[i]!) !== undefined).length;
  const submit = () => {
    if (answered === 0) return;
    const { answers, notes } = collect(questions, states);
    void decide({ behavior: "allow", answers, notes });
  };
  return (
    <div
      className={`${card} flex max-h-[60vh] flex-col border-accent/35`}
      role="dialog"
      aria-label="Questions"
    >
      <div className="flex items-center gap-2.5 px-4 pt-3.5">
        <MessageCircleQuestion size={16} className="shrink-0 text-accent" />
        <div className="text-base font-medium">
          {req.title || `${agentName} has ${questions.length === 1 ? "a question" : "questions"}`}
        </div>
      </div>
      <div className="mt-3 flex min-h-0 flex-col gap-5 overflow-y-auto px-4 pl-[42px]">
        {questions.map((q, i) =>
          states[i] ? (
            <QuestionField
              key={q.id || q.question}
              q={q}
              st={states[i]!}
              set={(p) => set(i, p)}
              onEnter={submit}
            />
          ) : null,
        )}
      </div>
      <div className="flex items-center gap-2 px-4 pt-3 pb-3.5">
        <div className="flex-1" />
        <Button
          size="sm"
          isDisabled={busy}
          onPress={() =>
            void decide({
              behavior: "deny",
              message:
                "The user chose not to answer. Go on with your best judgement, and say what you assumed.",
            })
          }
        >
          Skip
        </Button>
        <Button size="sm" variant="primary" isDisabled={busy || answered === 0} onPress={submit}>
          {answered < questions.length && answered > 0
            ? `Answer ${answered} of ${questions.length}`
            : "Answer"}
        </Button>
      </div>
    </div>
  );
}

const input =
  "selectable w-full rounded-ctl border border-strong bg-control px-2.5 text-base outline-none transition-colors placeholder:text-tertiary hover:border-quaternary focus:border-accent";

function QuestionField({
  q,
  st,
  set,
  onEnter,
}: {
  q: Question;
  st: QuestionState;
  set: (p: Partial<QuestionState>) => void;
  onEnter: () => void;
}) {
  const [noting, setNoting] = useState(false);
  const focused = q.options?.find((o) => st.picked.includes(o.label) && o.preview);
  return (
    <div>
      <div className="flex items-baseline gap-2">
        {q.header && (
          <span className="shrink-0 rounded-[5px] bg-selected px-1.5 py-px text-2xs font-medium text-secondary">
            {q.header}
          </span>
        )}
        <span className="selectable text-base font-medium">{q.question}</span>
      </div>
      {q.helper && <div className="mt-0.5 text-sm text-secondary">{q.helper}</div>}
      <div className="mt-2">
        {q.kind === "text" &&
          (q.secret ? (
            <input
              type="password"
              value={st.other}
              placeholder={q.placeholder || "Your answer"}
              aria-label={q.question}
              onChange={(e) => set({ other: e.target.value })}
              onKeyDown={(e) => {
                if (e.key === "Enter") onEnter();
              }}
              className={`${input} h-[30px]`}
            />
          ) : (
            <textarea
              value={st.other}
              rows={2}
              placeholder={q.placeholder || "Your answer"}
              aria-label={q.question}
              onChange={(e) => set({ other: e.target.value })}
              onKeyDown={(e) => {
                if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) onEnter();
              }}
              className={`${input} resize-none py-1.5`}
            />
          ))}
        {q.kind === "number" && (
          <div className="flex items-center gap-3">
            <input
              type="range"
              aria-label={q.question}
              min={q.min}
              max={q.max}
              step={q.step ?? 1}
              value={st.value ?? 0}
              onChange={(e) => set({ value: Number(e.target.value) })}
              className="w-64 cursor-pointer accent-[var(--accent)]"
            />
            <input
              type="number"
              aria-label={`${q.question} (number)`}
              min={q.min}
              max={q.max}
              step={q.step ?? 1}
              value={st.value ?? ""}
              onChange={(e) => set({ value: e.target.value === "" ? undefined : Number(e.target.value) })}
              className={`${input} h-[30px] w-24 tabular-nums`}
            />
            {q.unit && <span className="text-sm text-secondary">{q.unit}</span>}
          </div>
        )}
        {q.kind === "choice" && (
          <div className="flex flex-col gap-1.5">
            {q.options?.map((o, oi) => {
              const on = st.picked.includes(o.label);
              return (
                <button
                  key={o.label}
                  type="button"
                  aria-pressed={on}
                  onClick={() =>
                    set({
                      picked: q.multiSelect
                        ? on
                          ? st.picked.filter((x) => x !== o.label)
                          : [...st.picked, o.label]
                        : on
                          ? []
                          : [o.label],
                      ...(q.multiSelect ? {} : { other: "" }),
                    })
                  }
                  className={`flex cursor-pointer items-start gap-2.5 rounded-[8px] border px-3 py-2 text-left transition-colors ${on ? "border-accent bg-accent-soft" : "border-separator bg-control hover:border-strong hover:bg-hover"}`}
                >
                  <span className="mt-px w-4 shrink-0 text-sm text-tertiary tabular-nums">{oi + 1}</span>
                  <span className="min-w-0 flex-1">
                    <span className="block text-base">{o.label}</span>
                    {o.description && <span className="block text-sm text-secondary">{o.description}</span>}
                  </span>
                  <span
                    className={`mt-[3px] flex size-4 shrink-0 items-center justify-center border ${q.multiSelect ? "rounded-[4px]" : "rounded-full"} ${on ? "border-accent bg-accent" : "border-strong"}`}
                  >
                    {on &&
                      (q.multiSelect ? (
                        <span className="text-[10px] leading-none text-on-accent">✓</span>
                      ) : (
                        <span className="size-1.5 rounded-full bg-on-accent" />
                      ))}
                  </span>
                </button>
              );
            })}
            {q.other !== false && (
              <input
                type={q.secret ? "password" : "text"}
                value={st.other}
                placeholder={q.multiSelect ? "Something else (optional)" : "Something else…"}
                aria-label={`${q.question}: something else`}
                onChange={(e) =>
                  set({ other: e.target.value, ...(q.multiSelect || !e.target.value ? {} : { picked: [] }) })
                }
                onKeyDown={(e) => {
                  if (e.key === "Enter") onEnter();
                }}
                className={`${input} h-[30px]`}
              />
            )}
            {focused?.preview &&
              (focused.previewHtml ? (
                <iframe
                  title={`${focused.label} preview`}
                  sandbox=""
                  srcDoc={focused.preview}
                  className="h-48 w-full rounded-ctl border border-separator bg-white"
                />
              ) : (
                <div className="max-h-48 overflow-auto rounded-ctl border border-separator bg-raised px-3 py-2 text-sm">
                  <Markdown text={focused.preview} />
                </div>
              ))}
          </div>
        )}
      </div>
      <div className="mt-1.5">
        {noting ? (
          <input
            value={st.note}
            placeholder="A note with your answer"
            aria-label={`${q.question}: note`}
            onChange={(e) => set({ note: e.target.value })}
            className={`${input} h-[28px] text-sm`}
          />
        ) : (
          <button
            type="button"
            onClick={() => setNoting(true)}
            className="rounded-[5px] px-1 py-0.5 text-sm text-tertiary transition-colors hover:bg-hover hover:text-label"
          >
            + Add a note
          </button>
        )}
      </div>
    </div>
  );
}

/** How an approved plan can go on, per agent: most careful first. */
const planModes: Record<string, { mode: string; label: string }[]> = {
  claude: [
    { mode: "default", label: "Approve" },
    { mode: "acceptEdits", label: "Approve and accept edits" },
  ],
  codex: [
    { mode: "default", label: "Approve" },
    { mode: "bypassPermissions", label: "Approve with full access" },
  ],
};

function PlanCard({
  req,
  agent,
  agentName,
  busy,
  decide,
}: {
  req: PermissionRequest;
  agent: string;
  agentName: string;
  busy: boolean;
  decide: Decide;
}) {
  const [feedback, setFeedback] = useState<string | null>(null);
  const modes = planModes[agent] ?? planModes.claude!;
  return (
    <div className={`${card} flex max-h-[64vh] flex-col border-accent/35`} role="dialog" aria-label="Plan">
      <div className="flex items-center gap-2.5 px-4 pt-3.5">
        <ClipboardList size={16} className="shrink-0 text-accent" />
        <div className="text-base font-medium">{agentName}'s plan</div>
      </div>
      <div className="mx-4 mt-2.5 min-h-0 overflow-y-auto rounded-[8px] border border-separator bg-surface px-4 py-3">
        {req.plan ? (
          <Markdown text={req.plan} />
        ) : (
          <div className="text-sm text-secondary">No plan text came with it.</div>
        )}
      </div>
      {feedback !== null && (
        <div className="px-4 pt-2.5">
          <textarea
            autoFocus
            value={feedback}
            rows={3}
            onChange={(e) => setFeedback(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter" && (e.metaKey || e.ctrlKey) && feedback.trim())
                void decide({ behavior: "deny", message: feedback.trim() });
              if (e.key === "Escape") setFeedback(null);
            }}
            placeholder="What should change? It revises the plan and asks again."
            aria-label="Feedback on the plan"
            className="selectable w-full resize-none rounded-ctl border border-strong bg-control px-2.5 py-1.5 text-base outline-none placeholder:text-tertiary focus:border-accent"
          />
        </div>
      )}
      <div className="flex flex-wrap items-center gap-2 px-4 pt-3 pb-3.5">
        <div className="flex-1" />
        {feedback === null ? (
          <Button size="sm" isDisabled={busy} onPress={() => setFeedback("")}>
            Keep planning…
          </Button>
        ) : (
          <Button
            size="sm"
            isDisabled={busy || !feedback.trim()}
            onPress={() => void decide({ behavior: "deny", message: feedback.trim() })}
          >
            Send feedback
          </Button>
        )}
        {modes.map((m, i) => (
          <Button
            key={m.mode}
            size="sm"
            variant={i === modes.length - 1 ? "primary" : "secondary"}
            isDisabled={busy}
            onPress={() => void decide({ behavior: "allow", mode: m.mode })}
          >
            {m.label}
          </Button>
        ))}
      </div>
    </div>
  );
}
