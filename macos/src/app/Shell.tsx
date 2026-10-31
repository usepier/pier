import { Outlet } from "@tanstack/react-router";
import { type ReactNode, useState } from "react";
import { Group, Panel, Separator, useDefaultLayout } from "react-resizable-panels";
import { ConnectionBanner } from "./ConnectionBanner";
import { ToolbarSlot } from "./toolbar";

/**
 * The window: the sidebar under the traffic lights, and the content pane
 * with its toolbar row, which the page fills (<Toolbar>). Blank toolbar
 * space drags the window.
 */
export function Shell({
  sidebar,
  banners,
  fullWindow,
}: {
  sidebar: ReactNode;
  banners?: ReactNode;
  /** The page takes the whole window (its own sidebar included). */
  fullWindow?: boolean;
}) {
  const layout = useDefaultLayout({ id: "pier.shell", storage: localStorage });
  const [slot, setSlot] = useState<HTMLDivElement | null>(null);
  if (fullWindow) {
    return (
      <div className="flex h-full flex-col">
        <ConnectionBanner />
        {banners}
        <div className="min-h-0 flex-1">
          <Outlet />
        </div>
      </div>
    );
  }
  return (
    <Group orientation="horizontal" className="h-full" {...layout}>
      <Panel id="sidebar" defaultSize={248} minSize={200} maxSize={380} className="flex flex-col">
        <div data-tauri-drag-region className="h-[52px] shrink-0" />
        <div className="min-h-0 flex-1">{sidebar}</div>
      </Panel>
      <Separator className="w-px bg-separator outline-none data-[separator=active]:bg-accent" />
      <Panel id="main" className="flex min-w-0 flex-col bg-surface">
        <div
          ref={setSlot}
          data-tauri-drag-region
          className="flex h-[52px] shrink-0 items-center gap-3 border-b border-separator px-4"
        />
        <ConnectionBanner />
        {banners}
        <div className="min-h-0 flex-1">
          <ToolbarSlot.Provider value={slot}>
            <Outlet />
          </ToolbarSlot.Provider>
        </div>
      </Panel>
    </Group>
  );
}
