// One settings section per linked repository.
import { useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { rpc } from "@/api/client";
import { confirm } from "@/lib/confirm";
import { ago } from "@/lib/format";
import { trackJob } from "@/state/jobs";
import { useOverview } from "@/state/overview";
import { attempt, toast } from "@/state/toasts";
import { ActionButton, Button } from "@/ui/Button";
import { Select } from "@/ui/Select";
import type { Item, Section } from "./model";
import { RepoPrompts } from "./PromptItems";

/** One section per linked repository. */
export function useRepoSections(): Section[] {
  const o = useOverview();
  const qc = useQueryClient();
  const navigate = useNavigate();
  return (o?.repos ?? [])
    .filter((r) => r.checkout && !r.checkout.missing)
    .map((r) => {
      const path = r.checkout!.path;
      const items: Item[] = [
        {
          id: `${r.name}.checkout`,
          title: "Linked clone",
          description: <span className="selectable font-mono text-sm">{path}</span>,
          text: `clone checkout link ${path}`,
          control: (
            <ActionButton
              size="sm"
              variant="plain"
              action={async () => {
                if (
                  !(await confirm(
                    `Unlink ${r.name}?`,
                    "Its sessions keep running; new ones need a linked clone.",
                    { ok: "Unlink" },
                  ))
                )
                  return;
                await attempt(() => rpc.call("repos.unlink", { path }));
                void qc.invalidateQueries();
              }}
            >
              Unlink…
            </ActionButton>
          ),
        },
        {
          id: `${r.name}.pool`,
          title: "Pool size",
          description:
            "Parked sessions kept ready, so a new one starts in seconds. Each costs its disk while it waits.",
          hint: `${r.poolReady} ready${r.poolFilling ? `, ${r.poolFilling} filling` : ""}${r.poolStale ? `, ${r.poolStale} stale` : ""}`,
          text: "pool ready warm sessions size",
          control: (
            <Select
              label={`${r.name} pool size`}
              width={120}
              value={String(r.poolTarget)}
              onChange={(v) => void attempt(() => rpc.call("pool.setSize", { path, size: Number(v) }))}
              options={[0, 1, 2, 3, 4, 5, 6, 7, 8].map((n) => ({
                value: String(n),
                label: n === 0 ? "None" : String(n),
              }))}
            />
          ),
        },
        {
          id: `${r.name}.image`,
          title: "Session image",
          description: r.image
            ? `${r.imageKind === "prebuilt" ? "Toolchains and the repo, prebuilt" : "Toolchains"} — baked ${r.bakedAt ? ago(r.bakedAt) : "earlier"}.`
            : "None yet: new sessions run the repo's whole setup. A bake makes them start in a minute or two.",
          text: "image bake",
          control: (
            <ActionButton
              size="sm"
              isDisabled={r.baking}
              action={async () => {
                const job = await attempt(() => rpc.call("image.bake", { path }));
                if (job) {
                  trackJob(job);
                  toast(`Baking ${r.name}'s image`, "info");
                }
              }}
            >
              {r.baking ? "Baking…" : r.image ? "Bake again" : "Bake"}
            </ActionButton>
          ),
        },
        {
          id: `${r.name}.prompts`,
          title: "Prompts",
          description:
            "What pier asks the agent in this repo's sessions. Empty ones use your prompts for every repo.",
          text: "prompt wording fix ci address review comments create pull request onboard",
          children: <RepoPrompts repo={r.name} />,
        },
        {
          id: `${r.name}.cockpit`,
          title: "Sessions, cost and pull requests",
          description: "The repository's overview.",
          control: (
            <Button
              size="sm"
              variant="plain"
              onPress={() => navigate({ to: "/repo/$name", params: { name: r.name } })}
            >
              Open
            </Button>
          ),
        },
      ];
      return {
        id: `repo:${r.name}`,
        title: r.name,
        group: "Repositories" as const,
        icon: (
          <span className="flex size-[15px] items-center justify-center rounded-[4px] bg-selected text-[9px] font-semibold uppercase text-secondary">
            {r.name[0]}
          </span>
        ),
        description: r.origin,
        items,
      };
    });
}
