import { message as alert, ask as askYesNo } from "@tauri-apps/plugin-dialog";
import { inTauri } from "@/api/transport";

/** A native confirmation alert; resolves true when confirmed. */
export async function confirm(
  title: string,
  message: string,
  opts: { ok?: string; destructive?: boolean } = {},
): Promise<boolean> {
  if (!inTauri) return window.confirm(`${title}\n\n${message}`);
  return askYesNo(message, {
    title,
    kind: opts.destructive ? "warning" : "info",
    okLabel: opts.ok ?? "OK",
    cancelLabel: "Cancel",
  });
}

/**
 * A native alert with up to three choices (the last one cancels); resolves
 * the index of the one taken.
 */
export async function ask(
  title: string,
  text: string,
  choices: [string, string] | [string, string, string],
): Promise<number> {
  if (!inTauri) {
    if (window.confirm(`${title}\n\n${text}\n\nOK: ${choices[0]}`)) return 0;
    return choices.length - 1;
  }
  const buttons =
    choices.length === 3
      ? { yes: choices[0], no: choices[1], cancel: choices[2] }
      : { ok: choices[0], cancel: choices[1] };
  const r = await alert(text, { title, kind: "warning", buttons });
  const at = (choices as string[]).indexOf(r);
  if (at >= 0) return at;
  // Some platforms answer with the button's role rather than its label.
  const roles: Record<string, number> = { Yes: 0, Ok: 0, No: 1, Cancel: choices.length - 1 };
  return roles[r] ?? choices.length - 1;
}
