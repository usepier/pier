import { GitMerge, GitPullRequest, GitPullRequestClosed, GitPullRequestDraft } from "lucide-react";
import type { PRSummary } from "@/api/protocol.gen";

const checkTone: Record<string, string> = {
  passing: "bg-ok",
  failing: "bg-bad",
  pending: "bg-warn animate-pulse-soft",
};

/** "#12" with its state's icon and a dot for its checks. */
export function PRBadge({ pr, className = "" }: { pr: PRSummary; className?: string }) {
  const Icon =
    pr.state === "merged"
      ? GitMerge
      : pr.state === "closed"
        ? GitPullRequestClosed
        : pr.state === "draft"
          ? GitPullRequestDraft
          : GitPullRequest;
  const tone =
    pr.state === "merged"
      ? "text-merged"
      : pr.state === "closed"
        ? "text-bad"
        : pr.state === "draft"
          ? "text-tertiary"
          : "text-ok";
  const checks = pr.checks ? `checks ${pr.checks} (${pr.checksPassed}/${pr.checksTotal})` : "no checks";
  return (
    <span
      title={`#${pr.number} ${pr.title} — ${pr.state}, ${checks}`}
      className={`inline-flex shrink-0 items-center gap-1 text-xs tabular-nums ${className}`}
    >
      <Icon size={11} className={tone} />
      <span className="text-secondary">#{pr.number}</span>
      {pr.checks && (pr.state === "open" || pr.state === "draft") && (
        <span className={`size-1.5 rounded-full ${checkTone[pr.checks] ?? "bg-quaternary"}`} />
      )}
    </span>
  );
}
