import { Outlet } from "@tanstack/react-router";
import type { ReactNode } from "react";
import { Group, Panel, Separator, useDefaultLayout } from "react-resizable-panels";
import { ConnectionBanner } from "./ConnectionBanner";

/**
 * The window: a translucent sidebar under the traffic lights, and the
 * content pane with its toolbar row. Blank toolbar space drags the window.
 */
export function Shell({ sidebar, toolbar }: { sidebar: ReactNode; toolbar?: ReactNode }) {
  const layout = useDefaultLayout({ id: "pier.shell", storage: localStorage });
  return (
    <Group orientation="horizontal" className="h-full" {...layout}>
      <Panel id="sidebar" defaultSize={248} minSize={200} maxSize={380} className="flex flex-col">
        <div data-tauri-drag-region className="h-[52px] shrink-0" />
        <div className="min-h-0 flex-1">{sidebar}</div>
      </Panel>
      <Separator className="w-px bg-separator outline-none data-[separator=active]:bg-accent" />
      <Panel id="main" className="flex min-w-0 flex-col bg-surface">
        <div
          data-tauri-drag-region
          className="flex h-[52px] shrink-0 items-center gap-2 border-b border-separator px-4"
        >
          {toolbar}
        </div>
        <ConnectionBanner />
        <div className="min-h-0 flex-1">
          <Outlet />
        </div>
      </Panel>
    </Group>
  );
}
