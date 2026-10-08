import { relaunch } from "@tauri-apps/plugin-process";
import { check, type Update } from "@tauri-apps/plugin-updater";
import { useEffect, useState } from "react";
import { inTauri } from "@/api/transport";
import { toastError } from "@/state/toasts";
import { Banner } from "@/ui/Banner";
import { Button } from "@/ui/Button";

const every = 6 * 3600_000;

/** Says when a newer pier is out, and installs it on a restart. */
export function UpdateBanner() {
  const [update, setUpdate] = useState<Update | null>(null);
  const [progress, setProgress] = useState<number | null>(null);
  useEffect(() => {
    if (!inTauri || import.meta.env.DEV) return;
    const look = () =>
      void check()
        .then((u) => setUpdate(u))
        .catch(() => {}); // offline, or a build without an update key: try later
    look();
    const t = setInterval(look, every);
    return () => clearInterval(t);
  }, []);
  if (!update) return null;
  const install = async () => {
    let total = 0;
    let got = 0;
    setProgress(0);
    try {
      await update.downloadAndInstall((e) => {
        if (e.event === "Started") total = e.data.contentLength ?? 0;
        if (e.event === "Progress") {
          got += e.data.chunkLength;
          if (total) setProgress(got / total);
        }
      });
      await relaunch();
    } catch (e) {
      setProgress(null);
      toastError(e);
    }
  };
  return (
    <Banner
      tone="info"
      actions={
        <Button size="sm" variant="primary" isDisabled={progress !== null} onPress={() => void install()}>
          {progress === null ? "Restart to update" : `Updating… ${Math.round(progress * 100)}%`}
        </Button>
      }
    >
      pier {update.version} is out. Sessions keep running while the app restarts.
    </Banner>
  );
}
