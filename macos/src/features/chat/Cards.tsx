import { ClipboardList, MessageCircleQuestion, ShieldQuestion, SquareTerminal } from "lucide-react";
import { useEffect, useState } from "react";
import { rpc } from "@/api/client";
import type { PermissionRequest, Question } from "@/api/protocol.gen";
import { attempt } from "@/state/toasts";
import { Button } from "@/ui/Button";
import { answerOf, collect, emptyState, type QuestionState } from "./answers";
import { Markdown } from "./Markdown";

function pretty(json?: string): string {
  if (!json) return "";
  try {
    return JSON.stringify(JSON.parse(json), null, 2);
  } catch {
    return json;
  }
}

type Decide = (d: {
  behavior: "allow" | "deny";
  message?: string;
  answers?: Record<string, string>;
  notes?: Record<string, string>;
  mode?: string;
}) => Promise<void>;

/** What the agent waits on, pinned above the composer until answered. */
export function PendingCard({
  sessionId,
  req,
  agentName,
  onTerminal,
}: {
  sessionId: string;
  req: PermissionRequest;
  agentName: string;
  onTerminal?: () => void;
}) {
  const [busy, setBusy] = useState(false);
  const decide: Decide = async (d) => {
    setBusy(true);
    await attempt(() => rpc.call("chat.decide", { sessionId, requestId: req.id, ...d }));
    setBusy(false);
  };
  if (req.questions?.length) {
    return (
      <QuestionCard req={req} agentName={agentName} busy={busy} decide={decide} onTerminal={onTerminal} />
    );
  }
  if (req.tool === "ExitPlanMode") {
    return <PlanCard req={req} agentName={agentName} busy={busy} decide={decide} onTerminal={onTerminal} />;
  }
  return <PermissionCard req={req} agentName={agentName} busy={busy} decide={decide} />;
}

const card = "mx-auto mb-2 w-full max-w-3xl rounded-card border px-3.5 py-3";

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
  const [details, setDetails] = useState(false);
  return (
    <div className={`${card} border-warn/45 bg-warn/8`}>
      <div className="flex items-start gap-2.5">
        <ShieldQuestion size={16} className="mt-0.5 shrink-0 text-warn" />
        <div className="min-w-0 flex-1">
          <div className="text-base font-medium">
            {agentName} wants to use {req.tool}
          </div>
          {req.summary && req.summary !== req.tool && (
            <div className="selectable mt-1 font-mono text-[11.5px] break-all text-label/85">
              {req.summary}
            </div>
          )}
          {details && req.input && (
            <pre className="selectable mt-2 max-h-56 overflow-auto rounded-ctl bg-raised px-2 py-1.5 font-mono text-[11px] whitespace-pre-wrap break-all">
              {pretty(req.input)}
            </pre>
          )}
        </div>
      </div>
      <div className="mt-2.5 flex items-center gap-2 pl-[26px]">
        {req.input && req.input !== "{}" && (
          <button
            type="button"
            onClick={() => setDetails(!details)}
            className="text-xs text-secondary hover:text-label"
          >
            {details ? "Hide details" : "Details"}
          </button>
        )}
        <div className="flex-1" />
        <Button size="sm" isDisabled={busy} onPress={() => void decide({ behavior: "deny" })}>
          Deny
        </Button>
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
  onTerminal,
}: {
  req: PermissionRequest;
  agentName: string;
  busy: boolean;
  decide: Decide;
  onTerminal?: () => void;
}) {
  const questions = req.questions ?? [];
  const [states, setStates] = useState<QuestionState[]>(() => questions.map(emptyState));
  // A new request starts blank.
  // biome-ignore lint/correctness/useExhaustiveDependencies: keyed on the request
  useEffect(() => setStates(questions.map(emptyState)), [req.id]);
  const set = (i: number, p: Partial<QuestionState>) =>
    setStates((s) => s.map((x, j) => (j === i ? { ...x, ...p } : x)));
  const answered = questions.filter((q, i) => states[i] && answerOf(q, states[i]!) !== undefined).length;
  const submit = () => {
    const { answers, notes } = collect(questions, states);
    void decide({ behavior: "allow", answers, notes });
  };
  return (
    <div className={`${card} flex max-h-[55vh] flex-col border-accent/40 bg-accent-soft/40`}>
      <div className="flex items-center gap-2">
        <MessageCircleQuestion size={16} className="shrink-0 text-accent" />
        <div className="text-base font-medium">
          {req.title || `${agentName} has ${questions.length === 1 ? "a question" : "questions"}`}
        </div>
      </div>
      <div className="mt-2 flex min-h-0 flex-col gap-4 overflow-y-auto pl-[24px]">
        {questions.map((q, i) =>
          states[i] ? (
            <QuestionField key={q.question} q={q} st={states[i]!} set={(p) => set(i, p)} onEnter={submit} />
          ) : null,
        )}
      </div>
      <div className="mt-3 flex items-center gap-2 pl-[24px]">
        {onTerminal && (
          <button
            type="button"
            onClick={onTerminal}
            className="inline-flex items-center gap-1 text-xs text-secondary hover:text-label"
          >
            <SquareTerminal size={11} /> Terminal
          </button>
        )}
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
  const input =
    "selectable w-full rounded-ctl border border-separator bg-control px-2 text-base outline-none placeholder:text-tertiary focus:border-accent";
  return (
    <div>
      <div className="flex items-baseline gap-2">
        {q.header && (
          <span className="shrink-0 rounded bg-selected px-1.5 py-px text-2xs font-semibold text-secondary">
            {q.header}
          </span>
        )}
        <span className="selectable text-base font-medium">{q.question}</span>
      </div>
      {q.helper && <div className="mt-0.5 text-xs text-secondary">{q.helper}</div>}
      <div className="mt-2">
        {q.kind === "text" && (
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
        )}
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
              className="w-64 accent-[var(--accent)]"
            />
            <input
              type="number"
              aria-label={`${q.question} (number)`}
              min={q.min}
              max={q.max}
              step={q.step ?? 1}
              value={st.value ?? ""}
              onChange={(e) => set({ value: e.target.value === "" ? undefined : Number(e.target.value) })}
              className={`${input} h-7 w-24 tabular-nums`}
            />
            {q.unit && <span className="text-sm text-secondary">{q.unit}</span>}
          </div>
        )}
        {q.kind === "choice" && (
          <div className="flex flex-col gap-1.5">
            {q.options?.map((o) => {
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
                  className={`flex items-start gap-2.5 rounded-ctl border px-2.5 py-1.5 text-left ${on ? "border-accent bg-accent-soft" : "border-separator bg-control hover:bg-hover"}`}
                >
                  <span
                    className={`mt-[3px] flex size-3.5 shrink-0 items-center justify-center border ${q.multiSelect ? "rounded-[3px]" : "rounded-full"} ${on ? "border-accent bg-accent" : "border-tertiary"}`}
                  >
                    {on && (
                      <span
                        className={`${q.multiSelect ? "text-[9px] text-on-accent" : "size-1.5 rounded-full bg-on-accent"}`}
                      >
                        {q.multiSelect ? "✓" : ""}
                      </span>
                    )}
                  </span>
                  <span className="min-w-0">
                    <span className="block text-base">{o.label}</span>
                    {o.description && <span className="block text-xs text-secondary">{o.description}</span>}
                  </span>
                </button>
              );
            })}
            <input
              value={st.other}
              placeholder={q.multiSelect ? "Something else (optional)" : "Something else…"}
              aria-label={`${q.question}: something else`}
              onChange={(e) =>
                set({ other: e.target.value, ...(q.multiSelect || !e.target.value ? {} : { picked: [] }) })
              }
              onKeyDown={(e) => {
                if (e.key === "Enter") onEnter();
              }}
              className={`${input} h-7`}
            />
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
            className={`${input} h-7 text-sm`}
          />
        ) : (
          <button
            type="button"
            onClick={() => setNoting(true)}
            className="text-xs text-secondary hover:text-label"
          >
            Add a note
          </button>
        )}
      </div>
    </div>
  );
}

function PlanCard({
  req,
  agentName,
  busy,
  decide,
  onTerminal,
}: {
  req: PermissionRequest;
  agentName: string;
  busy: boolean;
  decide: Decide;
  onTerminal?: () => void;
}) {
  const [feedback, setFeedback] = useState<string | null>(null);
  return (
    <div className={`${card} flex max-h-[60vh] flex-col border-accent/40 bg-accent-soft/40`}>
      <div className="flex items-center gap-2">
        <ClipboardList size={16} className="shrink-0 text-accent" />
        <div className="text-base font-medium">{agentName}'s plan</div>
      </div>
      <div className="mt-2 min-h-0 overflow-y-auto rounded-ctl border border-separator bg-surface px-3.5 py-2.5">
        {req.plan ? (
          <Markdown text={req.plan} />
        ) : (
          <div className="text-sm text-secondary">The plan is in the agent's terminal.</div>
        )}
      </div>
      {feedback !== null && (
        <textarea
          autoFocus
          value={feedback}
          rows={3}
          onChange={(e) => setFeedback(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter" && (e.metaKey || e.ctrlKey) && feedback.trim())
              void decide({ behavior: "deny", message: feedback.trim() });
          }}
          placeholder="What should change? The agent revises the plan and asks again."
          aria-label="Feedback on the plan"
          className="selectable mt-2 w-full resize-none rounded-ctl border border-separator bg-control px-2 py-1.5 text-base outline-none placeholder:text-tertiary focus:border-accent"
        />
      )}
      <div className="mt-2.5 flex flex-wrap items-center gap-2">
        {onTerminal && (
          <button
            type="button"
            onClick={onTerminal}
            className="inline-flex items-center gap-1 text-xs text-secondary hover:text-label"
          >
            <SquareTerminal size={11} /> Terminal
          </button>
        )}
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
        <Button
          size="sm"
          isDisabled={busy}
          onPress={() => void decide({ behavior: "allow", mode: "default" })}
        >
          Approve
        </Button>
        <Button
          size="sm"
          variant="primary"
          isDisabled={busy}
          onPress={() => void decide({ behavior: "allow", mode: "acceptEdits" })}
        >
          Approve and accept edits
        </Button>
      </div>
    </div>
  );
}

/** The agent waits on something only its terminal shows. */
export function TerminalWaitCard({ agentName, onTerminal }: { agentName: string; onTerminal?: () => void }) {
  return (
    <div className="mx-auto mb-2 flex w-full max-w-3xl items-center gap-2.5 rounded-card border border-separator bg-raised px-3.5 py-2.5 text-sm">
      <SquareTerminal size={15} className="shrink-0 text-secondary" />
      <span className="flex-1">{agentName} is waiting for you in its terminal.</span>
      {onTerminal && (
        <Button size="sm" onPress={onTerminal}>
          Open terminal
        </Button>
      )}
    </div>
  );
}
