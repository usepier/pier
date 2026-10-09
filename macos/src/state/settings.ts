// pier's settings, as the app reads them (shared with the CLI and TUI).
import { useQuery } from "@tanstack/react-query";
import { rpc } from "@/api/client";

export function useSettings() {
  return useQuery({ queryKey: ["settings"], queryFn: () => rpc.call("settings.get", {}), staleTime: 30_000 });
}

/** One setting's value ("" while loading). */
export function useSetting(key: string): string {
  const q = useSettings();
  return q.data?.settings.find((s) => s.key === key)?.value ?? "";
}

/** How a new chat starts: the agent settings. */
export function useChatDefaults() {
  const q = useSettings();
  const get = (k: string) => q.data?.settings.find((s) => s.key === k)?.value ?? "";
  return (agent?: string) => {
    const a = agent || get("agents.default") || "claude";
    return {
      agent: a,
      model: get(`agents.${a}_model`),
      effort: get(`agents.${a}_effort`),
      mode: get(`agents.${a}_mode`) || "default",
    };
  };
}
