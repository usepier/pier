import { createHashHistory, createRootRoute, createRoute, createRouter } from "@tanstack/react-router";
import { CockpitPage } from "@/features/cockpit/CockpitPage";
import { HomePage } from "@/features/home/HomePage";
import { NewSessionPage } from "@/features/newSession/NewSessionPage";
import { SessionPage } from "@/features/session/SessionPage";
import { SettingsPage } from "@/features/settings/SettingsPage";
import { SetupPage } from "@/features/setup/SetupPage";
import { Root } from "./Root";

const root = createRootRoute({ component: Root });

const home = createRoute({ getParentRoute: () => root, path: "/", component: HomePage });
const repo = createRoute({ getParentRoute: () => root, path: "/repo/$name", component: CockpitPage });
const session = createRoute({ getParentRoute: () => root, path: "/session/$id", component: SessionPage });
const newSession = createRoute({
  getParentRoute: () => root,
  path: "/new",
  component: NewSessionPage,
  validateSearch: (s: Record<string, unknown>): { repo?: string; branch?: string; prompt?: string } => ({
    repo: typeof s.repo === "string" ? s.repo : undefined,
    branch: typeof s.branch === "string" ? s.branch : undefined,
    prompt: typeof s.prompt === "string" ? s.prompt : undefined,
  }),
});
const settings = createRoute({ getParentRoute: () => root, path: "/settings", component: SettingsPage });
const setup = createRoute({ getParentRoute: () => root, path: "/setup", component: SetupPage });

export const router = createRouter({
  routeTree: root.addChildren([home, repo, session, newSession, settings, setup]),
  history: createHashHistory(),
});

declare module "@tanstack/react-router" {
  interface Register {
    router: typeof router;
  }
}
