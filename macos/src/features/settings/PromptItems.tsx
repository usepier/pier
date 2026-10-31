// The prompts pier sends agents, as settings rows: the user's own wording
// for every repo, or a repo's own.
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { rpc } from "@/api/client";
import type { Prompt } from "@/api/protocol.gen";
import { promptsKey } from "@/state/prompts";
import { attempt, toast } from "@/state/toasts";
import { Button } from "@/ui/Button";
import type { Item } from "./model";

function PromptBox({ repo, p }: { repo: string; p: Prompt }) {
  const qc = useQueryClient();
  const [text, setText] = useState(p.own);
  useEffect(() => setText(p.own), [p.own]);
  const save = async (next: string) => {
    if (next.trim() === p.own.trim()) return;
    const ok = await attempt(() => rpc.call("prompts.set", { repo, key: p.key, text: next }));
    if (ok === undefined) return;
    // A user-wide change reaches every repo that falls back to it.
    await qc.invalidateQueries({ queryKey: repo ? promptsKey(repo) : ["prompts"] });
    toast(next.trim() ? "Saved" : "Back to the default", "ok");
  };
  return (
    <div className="flex flex-col gap-1.5">
      <textarea
        aria-label={`${p.title} prompt${repo ? ` for ${repo}` : ""}`}
        value={text}
        rows={3}
        placeholder={p.inherited}
        onChange={(e) => setText(e.target.value)}
        onBlur={() => void save(text)}
        className="selectable w-full resize-y rounded-ctl border border-strong bg-control px-2.5 py-2 text-sm leading-relaxed outline-none placeholder:text-tertiary focus:border-accent"
      />
      <div className="flex items-center gap-2 text-xs text-tertiary">
        <span className="flex-1">
          {p.own ? "Your wording." : repo ? "Empty: uses your prompt for every repo." : "Empty: uses pier's."}
          {p.key === "fixCI" || p.key === "addressComments" ? " {pr} is the pull request's number." : ""}
        </span>
        {p.own && (
          <Button size="sm" variant="plain" onPress={() => void save("")}>
            Reset
          </Button>
        )}
      </div>
    </div>
  );
}

/** The prompt rows for a repo ("" for every repo). */
export function usePromptItems(repo: string): Item[] {
  const q = useQuery({
    queryKey: promptsKey(repo),
    queryFn: () => rpc.call("prompts.get", { repo }),
  });
  return (q.data?.prompts ?? []).map((p) => ({
    id: `${repo || "all"}.prompt.${p.key}`,
    title: repo ? `Prompt: ${p.title}` : p.title,
    description: p.description,
    text: `prompt wording ${p.key} ${p.effective}`,
    children: <PromptBox repo={repo} p={p} />,
  }));
}

/** A repo's prompts, in one row: each with its title. */
export function RepoPrompts({ repo }: { repo: string }) {
  const q = useQuery({
    queryKey: promptsKey(repo),
    queryFn: () => rpc.call("prompts.get", { repo }),
  });
  return (
    <div className="flex flex-col gap-4">
      {(q.data?.prompts ?? []).map((p) => (
        <div key={p.key}>
          <div className="mb-1.5 text-sm font-medium">{p.title}</div>
          <PromptBox repo={repo} p={p} />
        </div>
      ))}
    </div>
  );
}
