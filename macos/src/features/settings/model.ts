// The settings page's model: sections in groups, each a list of rows a
// search can find by title, description or keywords.
import type { ReactNode } from "react";

export interface Item {
  id: string;
  title: string;
  description?: ReactNode;
  /** What the search matches besides the title (plain text). */
  text?: string;
  hint?: ReactNode;
  control?: ReactNode;
  children?: ReactNode;
}

export type Group = "This app" | "pier" | "Repositories" | "More";

export interface Section {
  id: string;
  title: string;
  group: Group;
  icon: ReactNode;
  description?: ReactNode;
  items: Item[];
}

/** Rows matching a query, by section. */
export function search(sections: Section[], query: string): { section: Section; items: Item[] }[] {
  const words = query.toLowerCase().split(/\s+/).filter(Boolean);
  if (!words.length) return [];
  const hit = (s: string) => words.every((w) => s.includes(w));
  return sections
    .map((section) => ({
      section,
      items: section.items.filter((i) => hit(`${section.title} ${i.title} ${i.text ?? ""}`.toLowerCase())),
    }))
    .filter((r) => r.items.length > 0);
}
