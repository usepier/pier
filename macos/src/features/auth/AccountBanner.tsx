import { useState } from "react";
import { rpc } from "@/api/client";
import { useOverview } from "@/state/overview";
import { toastError } from "@/state/toasts";
import { Banner } from "@/ui/Banner";
import { Button } from "@/ui/Button";

/** Says when the cloud account can't be reached, and fixes what can be fixed from here. */
export function AccountBanner() {
  const err = useOverview()?.error;
  const [lines, setLines] = useState<string[]>([]);
  const [busy, setBusy] = useState(false);
  if (!err) return null;
  if (err.kind === "auth_expired") {
    const signIn = async () => {
      setBusy(true);
      setLines([]);
      try {
        await rpc.call("auth.login", {}, { onProgress: (e) => setLines((l) => [...l.slice(-4), e.message]) });
      } catch (e) {
        toastError(e);
      }
      setBusy(false);
    };
    return (
      <Banner
        tone="warn"
        actions={
          <Button size="sm" variant="primary" isDisabled={busy} onPress={() => void signIn()}>
            {busy ? "Waiting for the browser…" : "Sign in"}
          </Button>
        }
      >
        <div>Your cloud sign-in expired{err.login ? ` — this runs \`${err.login.join(" ")}\`` : ""}.</div>
        {lines.map((l) => (
          <div key={l} className="selectable font-mono text-xs text-secondary">
            {l}
          </div>
        ))}
      </Banner>
    );
  }
  if (err.kind === "not_set_up") {
    return <Banner tone="info">pier isn't set up on this Mac yet — run `pier setup` in a terminal.</Banner>;
  }
  return <Banner tone="bad">Couldn't refresh: {err.message}</Banner>;
}
