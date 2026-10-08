import { useEffect } from "react";
import { Sidebar } from "@/features/sidebar/Sidebar";
import { useAppCommands } from "./appCommands";
import { Shell } from "./Shell";

export function Root() {
  useAppCommands();
  useEffect(() => {
    // The web view would reload on ⌘R; that is never what anyone wants here.
    const block = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "r") e.preventDefault();
    };
    window.addEventListener("keydown", block);
    return () => window.removeEventListener("keydown", block);
  }, []);
  return <Shell sidebar={<Sidebar />} />;
}
