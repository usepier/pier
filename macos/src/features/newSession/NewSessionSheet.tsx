import { useQuery } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { GitBranch } from "lucide-react";
import { useEffect, useMemo, useRef, useState } from "react";
import {
  Button as AriaButton,
  Checkbox,
  Label,
  ListBox,
  ListBoxItem,
  Popover,
  Select,
  SelectValue,
} from "react-aria-components";
import { rpc } from "@/api/client";
import { linkCheckout } from "@/features/repos/link";
import { branchProblem, plural } from "@/lib/format";
import { trackJob } from "@/state/jobs";
import { useOverview } from "@/state/overview";
import { attempt, toast } from "@/state/toasts";
import { useUI } from "@/state/ui";
import { Button } from "@/ui/Button";
import { Field } from "@/ui/Field";
import { Sheet } from "@/ui/Sheet";
import { Spinner } from "@/ui/Spinner";

const LINK = "\u0000link";

/** New Session: which clone, which branch, and what travels with it. */
export function NewSessionSheet() {
  const { open, repo: preset } = useUI((s) => s.newSession);
  const close = useUI((s) => s.closeNewSession);
  const o = useOverview();
  const navigate = useNavigate();
  const checkouts = (o?.checkouts ?? []).filter((c) => !c.missing);
  const [path, setPath] = useState<string>("");
  const [branch, setBranch] = useState("");
  const [base, setBase] = useState("");
  const [noPool, setNoPool] = useState(false);
  const [busy, setBusy] = useState(false);

  // Each opening starts clean, on the repo it was opened for.
  const wasOpen = useRef(false);
  useEffect(() => {
    const opening = open && !wasOpen.current;
    wasOpen.current = open;
    if (!opening) return;
    const fromPreset = checkouts.find((c) => c.name === preset)?.path;
    setPath(fromPreset ?? checkouts[0]?.path ?? "");
    setBranch("");
    setBase("");
    setNoPool(false);
  }, [open, preset, checkouts]);

  const info = useQuery({
    queryKey: ["inspect", path],
    queryFn: () => rpc.call("repos.inspect", { path }),
    enabled: open && !!path,
    staleTime: 2_000,
  });
  const taken = useMemo(() => new Set((o?.sessions ?? []).map((s) => s.name)), [o]);
  const problem = branch
    ? (branchProblem(branch) ?? (taken.has(branch) ? "a session with this name exists" : null))
    : null;
  const canCreate = !!path && !!branch && !problem && !busy;

  const create = async () => {
    if (!canCreate) return;
    setBusy(true);
    const job = await attempt(() =>
      rpc.call("sessions.create", { path, branch, base: base || undefined, noPool: noPool || undefined }),
    );
    setBusy(false);
    if (!job) return;
    trackJob(job);
    close();
    toast(`Creating ${branch}`, "info");
    navigate({ to: "/repo/$name", params: { name: info.data?.name ?? job.repo ?? "" } });
  };

  return (
    <Sheet open={open} onOpenChange={(v) => !v && close()} title="New Session">
      <div className="flex flex-col gap-4">
        <Select
          selectedKey={path || null}
          onSelectionChange={async (k) => {
            if (k === LINK) {
              const p = await linkCheckout();
              if (p) setPath(p);
              return;
            }
            setPath(String(k));
          }}
          className="flex flex-col gap-1"
        >
          <Label className="text-xs font-medium text-secondary">Repository</Label>
          <AriaButton className="flex h-7 items-center justify-between rounded-ctl border border-separator bg-control px-2 text-left text-base outline-none focus-visible:ring-2 focus-visible:ring-accent/40">
            <SelectValue className="truncate">
              {({ isPlaceholder }) =>
                isPlaceholder ? <span className="text-tertiary">Choose a linked clone</span> : undefined
              }
            </SelectValue>
            <span aria-hidden="true" className="text-tertiary">
              ⌄
            </span>
          </AriaButton>
          <Popover className="w-(--trigger-width) rounded-card border border-separator bg-surface p-1 shadow-xl outline-none">
            <ListBox className="outline-none">
              {checkouts.map((c) => (
                <ListBoxItem
                  key={c.path}
                  id={c.path}
                  textValue={c.name}
                  className="flex cursor-default flex-col rounded-ctl px-2 py-1 outline-none data-focused:bg-accent data-focused:text-on-accent"
                >
                  <span className="text-base">{c.name}</span>
                  <span className="truncate text-xs opacity-70">{c.path}</span>
                </ListBoxItem>
              ))}
              <ListBoxItem
                id={LINK}
                textValue="Link a repository"
                className="rounded-ctl px-2 py-1 text-base text-accent outline-none data-focused:bg-accent data-focused:text-on-accent"
              >
                Link a repository…
              </ListBoxItem>
            </ListBox>
          </Popover>
        </Select>

        {path && (
          <div className="flex items-start gap-2 rounded-ctl bg-raised px-3 py-2 text-sm">
            <GitBranch size={14} className="mt-0.5 shrink-0 text-secondary" />
            {info.isLoading ? (
              <Spinner />
            ) : info.data ? (
              <div className="min-w-0">
                <div>
                  From <b>{base || info.data.branch || "detached HEAD"}</b>
                  {!base && <span className="text-secondary"> @ {info.data.head}</span>}
                  {!base && info.data.dirty > 0 && (
                    <span className="text-secondary"> + {plural(info.data.dirty, "uncommitted change")}</span>
                  )}
                </div>
                <div className="text-xs text-secondary">
                  {info.data.base
                    ? `Pull requests target ${info.data.base}.`
                    : "No origin branch to compare against."}
                </div>
              </div>
            ) : (
              <span className="text-bad">{(info.error as Error)?.message}</span>
            )}
          </div>
        )}

        <Field
          label="Branch"
          value={branch}
          onChange={setBranch}
          placeholder="fix-login"
          mono
          autoFocus
          error={problem}
          hint="The session's new branch; also its name."
          onEnter={() => void create()}
        />
        <Field
          label="Start from"
          value={base}
          onChange={setBase}
          placeholder="HEAD — your checkout as it is now"
          mono
          hint="Another branch or commit, if not your current HEAD. Uncommitted changes only travel with HEAD."
        />
        <Checkbox isSelected={noPool} onChange={setNoPool} className="group flex items-center gap-2 text-sm">
          <span className="flex size-4 items-center justify-center rounded border border-separator bg-control group-data-selected:border-accent group-data-selected:bg-accent">
            <span className="hidden text-[10px] text-on-accent group-data-selected:block">✓</span>
          </span>
          Launch fresh instead of claiming a pooled session
        </Checkbox>

        <div className="mt-1 flex justify-end gap-2">
          <Button variant="plain" onPress={close}>
            Cancel
          </Button>
          <Button variant="primary" isDisabled={!canCreate} onPress={() => void create()}>
            {busy ? "Starting…" : "Create"}
          </Button>
        </div>
      </div>
    </Sheet>
  );
}
