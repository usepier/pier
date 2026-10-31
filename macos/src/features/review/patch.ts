// Reading a unified diff: the text of the lines a review comment is about.

/** The lines from..to of one side of a patch ("additions" is the new file). */
export function linesFromPatch(
  patch: string,
  side: "additions" | "deletions",
  from: number,
  to = from,
): string {
  const out: string[] = [];
  let oldN = 0;
  let newN = 0;
  for (const line of patch.split("\n")) {
    const h = line.match(/^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@/);
    if (h) {
      oldN = Number(h[1]);
      newN = Number(h[2]);
      continue;
    }
    if (!oldN && !newN) continue; // the header
    const kind = line[0];
    if (kind === "\\") continue; // "\ No newline at end of file"
    const text = line.slice(1);
    if (kind === "+" || kind === " ") {
      if (side === "additions" && newN >= from && newN <= to) out.push(text);
      newN++;
    }
    if (kind === "-" || kind === " ") {
      if (side === "deletions" && oldN >= from && oldN <= to) out.push(text);
      oldN++;
    }
  }
  return out.join("\n");
}
