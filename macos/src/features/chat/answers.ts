// A question card's state, turned into the answers the agent reads: one
// string per question, by question text.
import type { Question } from "@/api/protocol.gen";

export interface QuestionState {
  /** Chosen option labels (one for a single choice). */
  picked: string[];
  /** Typed instead of (or, for "Other", besides) the options. */
  other: string;
  /** A number question's value. */
  value?: number;
  note: string;
}

export function emptyState(q: Question): QuestionState {
  return {
    picked: [],
    other: "",
    value: q.kind === "number" ? (q.default ?? q.min ?? 0) : undefined,
    note: "",
  };
}

/** The answer to one question, or undefined while it has none. */
export function answerOf(q: Question, st: QuestionState): string | undefined {
  switch (q.kind) {
    case "text":
      return st.other.trim() || undefined;
    case "number":
      return st.value === undefined ? undefined : `${st.value}${q.unit ? ` ${q.unit}` : ""}`;
    default: {
      const parts = [...st.picked, ...(st.other.trim() ? [st.other.trim()] : [])];
      return parts.length ? parts.join(", ") : undefined;
    }
  }
}

export function collect(questions: Question[], states: QuestionState[]) {
  const answers: Record<string, string> = {};
  const notes: Record<string, string> = {};
  questions.forEach((q, i) => {
    const st = states[i]!;
    const a = answerOf(q, st);
    if (a !== undefined) answers[q.question] = a;
    if (st.note.trim()) notes[q.question] = st.note.trim();
  });
  return { answers, notes };
}
