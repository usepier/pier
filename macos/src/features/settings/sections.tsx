import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Bot, Cloud, Gauge, MessageSquareText, Moon, Palette, Rocket } from "lucide-react";
import { rpc } from "@/api/client";
import type { Settings } from "@/api/protocol.gen";
import { applyAccent } from "@/lib/theme";
import { attempt, toast } from "@/state/toasts";
import { useAppSections } from "./appSections";
import { icon } from "./icon";
import type { Section } from "./model";
import { useMoreSections } from "./moreSections";
import { codeSpans, pierItem, type Save } from "./PierSetting";
import { usePromptItems } from "./PromptItems";
import { useRepoSections } from "./repoSections";

const pierSections: { group: string; id: string; title: string; icon: typeof Bot; description?: string }[] = [
  {
    group: "agents",
    id: "agents",
    title: "Agents",
    icon: Bot,
    description:
      "How new chats start — in the app, the TUI and `pier <branch> -p`. A chat can change its model, effort and permissions as it goes.",
  },
  { group: "cloud", id: "cloud", title: "Cloud", icon: Cloud },
  { group: "sessions", id: "sessions", title: "New sessions", icon: Rocket },
  {
    group: "idle",
    id: "idle",
    title: "Idle & cost",
    icon: Moon,
    description: "Sessions park themselves: the VM stops and only its disk costs money.",
  },
  {
    group: "speed",
    id: "speed",
    title: "Speed & pools",
    icon: Gauge,
    description: "Session images and pools of parked sessions make new sessions start in seconds.",
  },
  { group: "app", id: "appearance", title: "Appearance", icon: Palette },
];

/** pier's own settings (shared with the CLI and TUI), by group. */
function usePierSections(settings: Settings | undefined, save: Save): Section[] {
  if (!settings) return [];
  return pierSections
    .map((d) => ({
      id: d.id,
      title: d.title,
      group: d.group === "app" ? ("This app" as const) : ("pier" as const),
      icon: icon(d.icon),
      description: d.description ? codeSpans(d.description) : undefined,
      items: settings.settings.filter((s) => s.group === d.group).map((s) => pierItem(s, save)),
    }))
    .filter((s) => s.items.length > 0);
}

/** Every section of the settings page, in nav order, and what they show besides. */
export function useSections(): {
  sections: Section[];
  loading: boolean;
  error?: unknown;
  extra: React.ReactNode;
} {
  const qc = useQueryClient();
  const q = useQuery({ queryKey: ["settings"], queryFn: () => rpc.call("settings.get", {}) });
  const save: Save = async (key, value) => {
    const next = await attempt(() => rpc.call("settings.set", { key, value }));
    if (!next) return;
    qc.setQueryData(["settings"], next);
    if (key === "theme") applyAccent(value);
    toast("Saved", "ok");
  };
  const app = useAppSections();
  const pier = usePierSections(q.data, save);
  const repos = useRepoSections();
  const more = useMoreSections();
  const promptItems = usePromptItems("");
  const prompts: Section[] = promptItems.length
    ? [
        {
          id: "prompts",
          title: "Prompts",
          group: "pier",
          icon: icon(MessageSquareText),
          description: "What pier asks the agent for you. A repository can have its own.",
          items: promptItems,
        },
      ]
    : [];
  const appearance = pier.filter((s) => s.group === "This app");
  return {
    sections: [
      ...app,
      ...appearance,
      ...pier.filter((s) => s.group === "pier"),
      ...prompts,
      ...repos,
      ...more.sections,
    ],
    loading: q.isLoading,
    error: q.error,
    extra: more.sheet,
  };
}
