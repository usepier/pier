// The prompts pier sends agents (pier's config: the user's own wording,
// and a repo's), read once and kept for a minute.
import { rpc } from "@/api/client";
import type { Prompt } from "@/api/protocol.gen";
import { queryClient } from "@/api/queryClient";

export const promptsKey = (repo: string) => ["prompts", repo] as const;

export function fetchPrompts(repo: string): Promise<Prompt[]> {
  return queryClient
    .fetchQuery({
      queryKey: promptsKey(repo),
      queryFn: () => rpc.call("prompts.get", { repo }),
      staleTime: 60_000,
    })
    .then((r) => r.prompts);
}

/** The text a prompt sends for a repo; pier's when it can't be read. */
export async function promptFor(repo: string, key: string, fallback: string): Promise<string> {
  try {
    return (await fetchPrompts(repo)).find((p) => p.key === key)?.effective || fallback;
  } catch {
    return fallback;
  }
}
