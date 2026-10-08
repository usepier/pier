import { createHashHistory, createRootRoute, createRoute, createRouter } from "@tanstack/react-router";
import { HomePage } from "@/features/home/HomePage";
import { SettingsPage } from "@/features/settings/SettingsPage";
import { Root } from "./Root";

const root = createRootRoute({ component: Root });

export const homeRoute = createRoute({ getParentRoute: () => root, path: "/", component: HomePage });
export const settingsRoute = createRoute({
  getParentRoute: () => root,
  path: "/settings",
  component: SettingsPage,
});

export const router = createRouter({
  routeTree: root.addChildren([homeRoute, settingsRoute]),
  history: createHashHistory(),
});

declare module "@tanstack/react-router" {
  interface Register {
    router: typeof router;
  }
}
