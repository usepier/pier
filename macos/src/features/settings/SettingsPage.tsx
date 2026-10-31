import { useRouter } from "@tanstack/react-router";
import { ArrowLeft, Search, X } from "lucide-react";
import { useEffect, useMemo, useRef, useState } from "react";
import { registerCommands } from "@/app/commands";
import { Kbd } from "@/ui/Kbd";
import { SettingsRow } from "@/ui/SettingsRow";
import { Spinner } from "@/ui/Spinner";
import { type Group, type Item, type Section, search } from "./model";
import { useSections } from "./sections";

const groups: Group[] = ["This app", "pier", "Repositories", "More"];
const lastSection = "pier.settings.section";

/**
 * Settings, the whole window: sections in a sidebar of their own — the
 * app's, pier's (shared with the CLI and TUI), one per repository, and
 * maintenance — and a search across all of them.
 */
export function SettingsPage() {
  const router = useRouter();
  const { sections, loading, error, extra } = useSections();
  const [active, setActive] = useState(() => localStorage.getItem(lastSection) ?? "general");
  const [query, setQuery] = useState("");
  const field = useRef<HTMLInputElement>(null);
  const results = useMemo(() => search(sections, query), [sections, query]);
  const current = sections.find((s) => s.id === active) ?? sections[0];

  useEffect(() => {
    localStorage.setItem(lastSection, active);
  }, [active]);
  useEffect(
    () =>
      registerCommands([
        {
          id: "settings.find",
          title: "Find in Settings",
          shortcut: "CmdOrCtrl+F",
          run: () => field.current?.focus(),
        },
      ]),
    [],
  );

  return (
    <div className="flex h-full">
      <nav
        aria-label="Settings"
        className="flex w-[260px] shrink-0 flex-col border-r border-separator bg-sidebar"
      >
        <div data-tauri-drag-region className="flex h-[52px] shrink-0 items-end justify-end px-3 pb-1">
          <button
            type="button"
            onClick={() =>
              router.history.canGoBack() ? router.history.back() : void router.navigate({ to: "/" })
            }
            className="flex items-center gap-1 rounded-[6px] px-2 py-1 text-sm text-secondary transition-colors hover:bg-hover hover:text-label"
          >
            <ArrowLeft size={13} /> Back
          </button>
        </div>
        <div className="px-3 pt-2 pb-3">
          <label className="flex h-[30px] items-center gap-2 rounded-ctl border border-strong bg-control px-2.5 text-secondary transition-colors focus-within:border-accent hover:border-quaternary">
            <Search size={13} className="shrink-0" />
            <input
              ref={field}
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              onKeyDown={(e) => e.key === "Escape" && setQuery("")}
              placeholder="Search settings"
              aria-label="Search settings"
              className="selectable min-w-0 flex-1 bg-transparent text-base text-label outline-none placeholder:text-tertiary focus-visible:outline-none"
            />
            {query ? (
              <button
                type="button"
                aria-label="Clear the search"
                onClick={() => setQuery("")}
                className="text-tertiary hover:text-label"
              >
                <X size={12} />
              </button>
            ) : (
              <Kbd>⌘F</Kbd>
            )}
          </label>
        </div>
        <div className="min-h-0 flex-1 overflow-y-auto px-2 pb-4">
          {groups.map((g) => {
            const list = sections.filter((s) => s.group === g);
            if (!list.length) return null;
            return (
              <div key={g} className="mb-4">
                <div className="px-2 pb-1 text-sm text-tertiary">{g}</div>
                {list.map((s) => {
                  const on = !query && current?.id === s.id;
                  return (
                    <button
                      key={s.id}
                      type="button"
                      aria-current={on ? "page" : undefined}
                      onClick={() => {
                        setQuery("");
                        setActive(s.id);
                      }}
                      className={`flex w-full items-center gap-2.5 rounded-[7px] px-2 py-[6px] text-left text-base transition-colors ${on ? "bg-selected text-label" : "text-secondary hover:bg-hover hover:text-label"}`}
                    >
                      <span aria-hidden="true" className="flex w-4 shrink-0 justify-center">
                        {s.icon}
                      </span>
                      <span className="truncate">{s.title}</span>
                    </button>
                  );
                })}
              </div>
            );
          })}
        </div>
      </nav>
      <main className="min-w-0 flex-1 overflow-y-auto bg-surface">
        <div data-tauri-drag-region className="h-[52px]" />
        <div className="mx-auto max-w-[880px] px-10 pb-16">
          {loading ? (
            <div className="flex justify-center py-24">
              <Spinner />
            </div>
          ) : error ? (
            <div className="selectable py-24 text-center text-bad">{(error as Error).message}</div>
          ) : query ? (
            results.length ? (
              results.map((r) => (
                <SectionView key={r.section.id} section={r.section} items={r.items} compact />
              ))
            ) : (
              <div className="py-24 text-center text-secondary">Nothing matches “{query}”.</div>
            )
          ) : current ? (
            <SectionView section={current} items={current.items} />
          ) : null}
        </div>
      </main>
      {extra}
    </div>
  );
}

function SectionView({ section: s, items, compact }: { section: Section; items: Item[]; compact?: boolean }) {
  return (
    <section className={compact ? "mb-10" : ""}>
      <h1 className={`font-display font-semibold text-label ${compact ? "text-md" : "text-xl"}`}>
        {s.title}
      </h1>
      {s.description && !compact && <p className="mt-1.5 text-base text-secondary">{s.description}</p>}
      <div className={compact ? "mt-1" : "mt-4"}>
        {items.map((i) => (
          <SettingsRow
            key={i.id}
            id={i.id}
            title={i.title}
            description={i.description}
            hint={i.hint}
            control={i.control}
          >
            {i.children}
          </SettingsRow>
        ))}
      </div>
    </section>
  );
}
