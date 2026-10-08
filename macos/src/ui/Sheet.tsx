import type { ReactNode } from "react";
import { Dialog, Heading, Modal, ModalOverlay } from "react-aria-components";

/** A modal sheet that slides down from the top of the window, like a Mac sheet. */
export function Sheet({
  open,
  onOpenChange,
  title,
  children,
  width = 520,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: string;
  children: ReactNode;
  width?: number;
}) {
  return (
    <ModalOverlay
      isOpen={open}
      onOpenChange={onOpenChange}
      isDismissable
      className="fixed inset-0 z-50 flex justify-center bg-black/15"
    >
      <Modal
        style={{ width }}
        className="mt-[52px] max-h-[80vh] self-start overflow-auto rounded-b-card border border-t-0 border-separator bg-surface shadow-2xl outline-none"
      >
        <Dialog className="outline-none">
          <Heading slot="title" className="px-5 pt-4 text-md font-semibold">
            {title}
          </Heading>
          <div className="px-5 pt-3 pb-5">{children}</div>
        </Dialog>
      </Modal>
    </ModalOverlay>
  );
}
