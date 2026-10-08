// The native menu bar, built from the command registry.
import { Menu, MenuItem, PredefinedMenuItem, Submenu } from "@tauri-apps/api/menu";
import { inTauri } from "@/api/transport";
import { allCommands, type Command, type MenuSection, runCommand } from "./commands";

const sections: { id: MenuSection; title: string }[] = [
  { id: "file", title: "File" },
  { id: "edit", title: "Edit" },
  { id: "view", title: "View" },
  { id: "session", title: "Session" },
  { id: "repo", title: "Repository" },
  { id: "go", title: "Go" },
];

async function item(c: Command) {
  return MenuItem.new({
    id: c.id,
    text: c.title,
    accelerator: c.shortcut,
    enabled: c.enabled?.() ?? true,
    action: () => runCommand(c.id),
  });
}

let building: Promise<void> | null = null;
let again = false;

/** (Re)builds the menu bar. Cheap enough to call when commands change. */
export function buildMenu(): Promise<void> {
  if (!inTauri) return Promise.resolve();
  if (building) {
    again = true;
    return building;
  }
  building = (async () => {
    const cmds = allCommands();
    const by = (s: MenuSection) => cmds.filter((c) => c.menu === s);
    const sep = () => PredefinedMenuItem.new({ item: "Separator" });

    const app = await Submenu.new({
      text: "Pier",
      items: [
        await PredefinedMenuItem.new({ item: { About: null } }),
        await sep(),
        ...(await Promise.all(by("app").map(item))),
        await sep(),
        await PredefinedMenuItem.new({ item: "Services" }),
        await sep(),
        await PredefinedMenuItem.new({ item: "Hide" }),
        await PredefinedMenuItem.new({ item: "HideOthers" }),
        await PredefinedMenuItem.new({ item: "ShowAll" }),
        await sep(),
        await PredefinedMenuItem.new({ item: "Quit" }),
      ],
    });
    const menus = [app];
    for (const s of sections) {
      let items: (MenuItem | PredefinedMenuItem)[] = await Promise.all(by(s.id).map(item));
      if (s.id === "edit") {
        items = [
          await PredefinedMenuItem.new({ item: "Undo" }),
          await PredefinedMenuItem.new({ item: "Redo" }),
          await sep(),
          await PredefinedMenuItem.new({ item: "Cut" }),
          await PredefinedMenuItem.new({ item: "Copy" }),
          await PredefinedMenuItem.new({ item: "Paste" }),
          await PredefinedMenuItem.new({ item: "SelectAll" }),
          ...(items.length ? [await sep(), ...items] : []),
        ];
      }
      if (s.id === "view") {
        items = [...items, await sep(), await PredefinedMenuItem.new({ item: "Fullscreen" })];
      }
      if (items.length) menus.push(await Submenu.new({ text: s.title, items }));
    }
    const win = await Submenu.new({
      text: "Window",
      items: [
        await PredefinedMenuItem.new({ item: "Minimize" }),
        await PredefinedMenuItem.new({ item: "Maximize" }),
        await sep(),
        await PredefinedMenuItem.new({ item: "CloseWindow" }),
      ],
    });
    menus.push(win);
    const help = by("help");
    let helpMenu: Submenu | null = null;
    if (help.length) {
      helpMenu = await Submenu.new({ text: "Help", items: await Promise.all(help.map(item)) });
      menus.push(helpMenu);
    }
    const menu = await Menu.new({ items: menus });
    await menu.setAsAppMenu();
    await win.setAsWindowsMenuForNSApp();
    if (helpMenu) await helpMenu.setAsHelpMenuForNSApp();
  })().finally(() => {
    building = null;
    if (again) {
      again = false;
      void buildMenu();
    }
  });
  return building;
}
