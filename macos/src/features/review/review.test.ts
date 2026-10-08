import { describe, expect, it } from "vitest";
import { reviewMessage } from "@/state/comments";
import { linesFromPatch } from "./patch";

const patch = `diff --git a/a.ts b/a.ts
--- a/a.ts
+++ b/a.ts
@@ -10,4 +10,5 @@ export function pay() {
 const a = 1;
-const b = 2;
+const b = 3;
+const c = 4;
 return a + b;
\\ No newline at end of file`;

describe("patch lines", () => {
  it("reads either side by its own line numbers", () => {
    expect(linesFromPatch(patch, "additions", 11, 12)).toBe("const b = 3;\nconst c = 4;");
    expect(linesFromPatch(patch, "deletions", 11)).toBe("const b = 2;");
    expect(linesFromPatch(patch, "additions", 13)).toBe("return a + b;");
  });
});

describe("review message", () => {
  it("quotes each comment's lines under its file", () => {
    const msg = reviewMessage([
      { id: "2", path: "a.ts", side: "additions", line: 12, text: "drop c", code: "const c = 4;" },
      { id: "1", path: "a.ts", side: "deletions", line: 11, text: "why?" },
    ]);
    expect(msg).toContain("a.ts:11 (removed lines)\nwhy?");
    expect(msg).toContain("a.ts:12\n> const c = 4;\ndrop c");
    expect(msg.indexOf(":11")).toBeLessThan(msg.indexOf(":12"));
  });
});

import { buildTree } from "@/features/files/tree";

describe("file tree", () => {
  it("nests paths, folders first", () => {
    const t = buildTree(["b.txt", "src/z.go", "src/app/main.go", "a.txt"]);
    expect(t.map((n) => n.name)).toEqual(["src", "a.txt", "b.txt"]);
    expect(t[0]!.children!.map((n) => n.name)).toEqual(["app", "z.go"]);
    expect(t[0]!.children![0]!.children![0]!.path).toBe("src/app/main.go");
  });
});
