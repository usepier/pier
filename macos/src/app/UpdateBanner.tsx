import { useEffect } from "react";
import { toastError } from "@/state/toasts";
import { useUpdates, watchUpdates } from "@/state/updates";
import { Banner } from "@/ui/Banner";
import { Button } from "@/ui/Button";

/** Says when a newer pier is out, and installs it on a restart. */
export function UpdateBanner() {
  const update = useUpdates((u) => u.update);
  const progress = useUpdates((u) => u.progress);
  const install = useUpdates((u) => u.install);
  useEffect(() => watchUpdates(), []);
  if (!update) return null;
  return (
    <Banner
      tone="info"
      actions={
        <Button
          size="sm"
          variant="primary"
          isDisabled={progress !== undefined}
          onPress={() => void install().catch(toastError)}
        >
          {progress === undefined ? "Restart to update" : `Updating… ${Math.round(progress * 100)}%`}
        </Button>
      }
    >
      pier {update.version} is out. Sessions keep running while the app restarts.
    </Banner>
  );
}
