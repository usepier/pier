// Sends a session's review comments (diffs and files) to its agent as one
// message, headed the way the user's prompts say.
import type { Session } from "@/api/protocol.gen";
import { reviewHeader, reviewMessage, useComments } from "@/state/comments";
import { sendToAgent } from "@/state/currentChat";
import { promptFor } from "@/state/prompts";
import { toast } from "@/state/toasts";

export async function sendReview(s: Session) {
  const sent = useComments.getState().comments[s.id] ?? [];
  if (!sent.length) return;
  const header = await promptFor(s.repo, "review", reviewHeader);
  sendToAgent(s, reviewMessage(sent, header));
  // Only what went: a comment written meanwhile stays.
  for (const c of sent) useComments.getState().remove(s.id, c.id);
  toast(`Sent ${sent.length} ${sent.length === 1 ? "comment" : "comments"} to the agent`, "ok");
}
