import { Copy } from "lucide-react";
import { toast } from "@/state/toasts";
import { Button } from "@/ui/Button";

export function Nav({
  back,
  next,
  nextLabel = "Continue",
  canNext = true,
}: {
  back?: () => void;
  next?: () => void;
  nextLabel?: string;
  canNext?: boolean;
}) {
  return (
    <div className="mt-2 flex justify-between">
      {back ? <Button onPress={back}>Back</Button> : <span />}
      {next && (
        <Button variant="primary" isDisabled={!canNext} onPress={next}>
          {nextLabel}
        </Button>
      )}
    </div>
  );
}

export function CopyCommand({ text }: { text: string }) {
  return (
    <span className="inline-flex items-center gap-1">
      <code className="selectable rounded bg-selected px-1.5 py-0.5 font-mono text-[11.5px]">{text}</code>
      <button
        type="button"
        aria-label="Copy"
        onClick={() => {
          void navigator.clipboard.writeText(text);
          toast("Copied", "ok");
        }}
        className="text-tertiary hover:text-label"
      >
        <Copy size={11} />
      </button>
    </span>
  );
}
