import { MessageSquare, SquareTerminal } from "lucide-react";
import { usePrefs } from "@/state/prefs";
import { Kbd } from "@/ui/Kbd";
import { Select } from "@/ui/Select";
import { Switch } from "@/ui/Switch";
import { icon } from "./icon";
import type { Section } from "./model";

export const minutes = [
  { value: "0", label: "Right away" },
  { value: "60000", label: "After 1 minute" },
  { value: "300000", label: "After 5 minutes" },
  { value: "600000", label: "After 10 minutes" },
  { value: "1800000", label: "After 30 minutes" },
];

/** Settings of the app itself, kept on this Mac. */
export function useAppSections(): Section[] {
  const p = usePrefs();
  return [
    {
      id: "general",
      title: "General",
      group: "This app",
      icon: icon(MessageSquare),
      items: [
        {
          id: "send",
          title: "Send messages with",
          description: "Which key sends a message from the composer.",
          hint:
            p.sendKey === "enter" ? (
              <>
                Use <Kbd>⇧</Kbd>
                <Kbd>⏎</Kbd> for new lines
              </>
            ) : (
              <>
                Use <Kbd>⏎</Kbd> for new lines
              </>
            ),
          text: "enter return send key composer",
          control: (
            <Select
              label="Send messages with"
              width={180}
              value={p.sendKey}
              onChange={(v) => p.set({ sendKey: v })}
              options={[
                { value: "enter", label: "Enter" },
                { value: "cmdEnter", label: "⌘ Enter" },
              ]}
            />
          ),
        },
        {
          id: "followUp",
          title: "Follow-up behavior",
          description:
            "A message sent while the agent works steers its turn now, or waits in the queue for the turn to end.",
          hint: (
            <>
              Use <Kbd>⌘</Kbd>
              <Kbd>⇧</Kbd>
              <Kbd>⏎</Kbd> for the other
            </>
          ),
          text: "steer queue mid-turn follow up",
          control: (
            <Select
              label="Follow-up behavior"
              width={180}
              value={p.followUp}
              onChange={(v) => p.set({ followUp: v })}
              options={[
                { value: "steer", label: "Steer" },
                { value: "queue", label: "Queue" },
              ]}
            />
          ),
        },
        {
          id: "expandTools",
          title: "Show every tool call",
          description:
            "Keep each step the agent takes in view, instead of folding a run of them into one line.",
          text: "tools steps collapse fold expand",
          control: (
            <Switch
              label="Show every tool call"
              isSelected={p.expandTools}
              onChange={(v) => p.set({ expandTools: v })}
            />
          ),
        },
        {
          id: "startWithChat",
          title: "Start new sessions with a chat",
          description:
            "New Session opens on a chat with your default agent; its first message goes in once the session is set up.",
          text: "new session agent chat prompt",
          control: (
            <Switch
              label="Start new sessions with a chat"
              isSelected={p.startWithChat}
              onChange={(v) => p.set({ startWithChat: v })}
            />
          ),
        },
        {
          id: "autoRemove",
          title: "Remove merged sessions",
          description:
            "Once a session's pull request merges — only when everything in it is committed and merged.",
          text: "cleanup merged pull request remove",
          control: (
            <Switch
              label="Remove merged sessions"
              isSelected={p.autoRemoveMerged}
              onChange={(v) => p.set({ autoRemoveMerged: v })}
            />
          ),
        },
      ],
    },
    {
      id: "terminals",
      title: "Terminals",
      group: "This app",
      icon: icon(SquareTerminal),
      description:
        "A terminal tab attached to a session keeps it awake; these let sessions park when you look away.",
      items: [
        {
          id: "hiddenDetach",
          title: "Detach a hidden terminal",
          description: "A terminal tab you aren't looking at lets go of its session after this long.",
          text: "terminal detach hidden park",
          control: (
            <Select
              label="Detach a hidden terminal"
              width={200}
              value={String(p.hiddenDetachMs)}
              onChange={(v) => p.set({ hiddenDetachMs: Number(v) })}
              options={minutes}
            />
          ),
        },
        {
          id: "backgroundDetach",
          title: "Detach every terminal in the background",
          description:
            "Once the app has been out of sight this long, every terminal lets go and sessions can park.",
          text: "terminal detach background park",
          control: (
            <Select
              label="Detach every terminal in the background"
              width={200}
              value={String(p.backgroundDetachMs)}
              onChange={(v) => p.set({ backgroundDetachMs: Number(v) })}
              options={minutes}
            />
          ),
        },
        {
          id: "fontSize",
          title: "Font size",
          description: "The terminals' text size.",
          text: "terminal font size",
          control: (
            <Select
              label="Terminal font size"
              width={120}
              value={String(p.terminalFontSize)}
              onChange={(v) => p.set({ terminalFontSize: Number(v) })}
              options={[11, 12, 12.5, 13, 14, 15, 16].map((n) => ({ value: String(n), label: `${n} pt` }))}
            />
          ),
        },
      ],
    },
  ];
}
