// pier's settings, as the app reads them (shared with the CLI and TUI).
import { useQuery } from "@tanstack/react-query";
import { rpc } from "@/api/client";
import type { Settings } from "@/api/protocol.gen";
import { queryClient } from "@/api/queryClient";

export function useSettings() {
  return useQuery({ queryKey: ["settings"], queryFn: () => rpc.call("settings.get", {}), staleTime: 30_000 });
}

/** One setting's value ("" while loading). */
export function useSetting(key: string): string {
  const q = useSettings();
  return q.data?.settings.find((s) => s.key === key)?.value ?? "";
}

/**
 * How a new chat starts: the default model (its agent, model and effort),
 * or another agent's own default; each agent's permission mode.
 */
export function chatDefaults(settings: Settings | undefined, agent?: string) {
  const get = (k: string) => settings?.settings.find((s) => s.key === k)?.value ?? "";
  const def = get("agents.default") || "claude";
  const a = agent || def;
  return {
    agent: a,
    model: a === def ? get("agents.model") : "",
    effort: a === def ? get("agents.effort") : "",
    mode: get(`agents.${a}_mode`) || "default",
  };
}

/** chatDefaults, with the settings as they load. */
export function useChatDefaults() {
  const q = useSettings();
  return (agent?: string) => chatDefaults(q.data, agent);
}

/**
 * The permission mode last picked for a chat becomes its agent's default
 * for new chats (the agents setting, which the TUI and CLI use too): a
 * habit sticks without a trip to Settings. Plan mode is a task's choice,
 * not a habit, so it doesn't.
 */
export async function rememberMode(agent: string, mode: string) {
  if (!agent || !mode || mode === "plan") return;
  const key = `agents.${agent}_mode`;
  const now = queryClient.getQueryData<Settings>(["settings"])?.settings.find((s) => s.key === key)?.value;
  if (now === mode) return;
  const next = await rpc.call("settings.set", { key, value: mode }).catch(() => undefined);
  if (next) queryClient.setQueryData(["settings"], next);
}
