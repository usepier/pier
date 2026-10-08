import { createHashHistory, createRootRoute, createRoute, createRouter } from "@tanstack/react-router";
import { CockpitPage } from "@/features/cockpit/CockpitPage";
import { HomePage } from "@/features/home/HomePage";
import { SessionPage } from "@/features/session/SessionPage";
import { SettingsPage } from "@/features/settings/SettingsPage";
import { Root } from "./Root";

const root = createRootRoute({ component: Root });

const home = createRoute({ getParentRoute: () => root, path: "/", component: HomePage });
const repo = createRoute({ getParentRoute: () => root, path: "/repo/$name", component: CockpitPage });
const session = createRoute({ getParentRoute: () => root, path: "/session/$id", component: SessionPage });
const settings = createRoute({ getParentRoute: () => root, path: "/settings", component: SettingsPage });

export const router = createRouter({
  routeTree: root.addChildren([home, repo, session, settings]),
  history: createHashHistory(),
});

declare module "@tanstack/react-router" {
  interface Register {
    router: typeof router;
  }
}
