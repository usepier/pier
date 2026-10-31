// The app's confirmation modal: one question at a time, its choices as
// buttons — Return takes the first, Escape the last (cancel).
import { Dialog, Heading, Modal, ModalOverlay } from "react-aria-components";
import { useAsking } from "@/lib/confirm";
import { Button } from "@/ui/Button";

export function ConfirmHost() {
  const a = useAsking((s) => s.asks[0]);
  if (!a) return null;
  const cancel = a.choices.length - 1;
  return (
    <ModalOverlay
      isOpen
      isDismissable
      onOpenChange={(open) => !open && a.answer(cancel)}
      className="fixed inset-0 z-[60] flex items-start justify-center bg-black/30 pt-[18vh]"
    >
      <Modal className="pop-in w-[420px] rounded-[12px] border border-strong bg-popover shadow-(--shadow-pop) outline-none">
        <Dialog role="alertdialog" className="flex flex-col gap-2 p-5 outline-none">
          <Heading slot="title" className="text-md font-semibold">
            {a.title}
          </Heading>
          {a.text && <p className="text-base leading-relaxed text-secondary">{a.text}</p>}
          <div className="mt-4 flex justify-end gap-2">
            {a.choices
              .map((c, i) => ({ c, i }))
              .reverse()
              .map(({ c, i }) => (
                <Button
                  key={i}
                  autoFocus={i === 0}
                  variant={i === 0 ? (a.destructive ? "danger" : "primary") : "secondary"}
                  onPress={() => a.answer(i)}
                >
                  {c}
                </Button>
              ))}
          </div>
        </Dialog>
      </Modal>
    </ModalOverlay>
  );
}
