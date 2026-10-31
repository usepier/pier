// The start of a repo's or a session's toolbar: where you are, as links —
// the repo, its page on the web, its folder on this Mac.
import { Folder, Globe } from "lucide-react";
import type { ReactNode } from "react";
import { Button as AriaButton } from "react-aria-components";
import type { Repo } from "@/api/protocol.gen";
import { openExternal, originUrl, showInFinder, tildePath } from "@/lib/open";
import { Tip } from "@/ui/Tooltip";

/** A repo's initial in a tile, as the sidebar shows it. */
export function RepoTile({ name, className = "" }: { name: string; className?: string }) {
  return (
    <span
      aria-hidden="true"
      className={`flex size-[22px] shrink-0 items-center justify-center rounded-[6px] bg-selected text-xs font-semibold text-secondary uppercase ${className}`}
    >
      {name.slice(0, 1)}
    </span>
  );
}

/** One link in the toolbar: it highlights under the pointer, and says where it goes. */
export function Crumb({
  tip,
  onPress,
  children,
  className = "",
}: {
  tip: string;
  onPress: () => void;
  children: ReactNode;
  className?: string;
}) {
  return (
    <Tip content={tip}>
      <AriaButton
        onPress={onPress}
        className={`flex h-7 min-w-0 shrink items-center gap-1.5 rounded-ctl px-2 text-left outline-none transition-colors hover:bg-hover focus-visible:ring-2 focus-visible:ring-accent/50 ${className}`}
      >
        {children}
      </AriaButton>
    </Tip>
  );
}

/** The repo's web page and its folder, as links. */
export function RepoLinks({ repo }: { repo: Repo }) {
  const url = originUrl(repo.origin);
  const host = repo.origin?.split("/")[0];
  return (
    <>
      {url && (
        <Crumb tip={`Open on ${host}`} onPress={() => openExternal(url)} className="text-sm text-secondary">
          <Globe size={14} className="shrink-0 text-tertiary" />
          <span className="truncate">{repo.origin!.slice(host!.length + 1)}</span>
        </Crumb>
      )}
      {repo.checkout && (
        <Crumb
          tip="Show in Finder"
          onPress={() => showInFinder(repo.checkout!.path)}
          className="text-sm text-secondary"
        >
          <Folder size={14} className="shrink-0 text-tertiary" />
          <span className="truncate">{tildePath(repo.checkout.path)}</span>
        </Crumb>
      )}
    </>
  );
}
