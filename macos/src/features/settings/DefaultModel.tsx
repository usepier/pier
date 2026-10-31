// The default model as one picker — the same as a chat's — over the three
// pier settings it is: its agent, the model and its effort.
import type { Settings } from "@/api/protocol.gen";
import { ModelPicker } from "@/features/chat/ModelPicker";
import type { SaveAll } from "./sections";

export function DefaultModel({ settings, save }: { settings: Settings; save: SaveAll }) {
  const get = (k: string) => settings.settings.find((s) => s.key === k)?.value ?? "";
  const agent = get("agents.default") || "claude";
  return (
    <ModelPicker
      value={{ agent, model: get("agents.model"), effort: get("agents.effort"), fast: false }}
      withFast={false}
      placement="bottom end"
      onChange={(p) => {
        const next = new Map<string, string>();
        // Another agent's model starts at its own default effort.
        if (p.agent && p.agent !== agent) next.set("agents.default", p.agent).set("agents.effort", "");
        if (p.model !== undefined) next.set("agents.model", p.model);
        if (p.effort !== undefined) next.set("agents.effort", p.effort);
        if (next.size) void save([...next]);
      }}
    />
  );
}
