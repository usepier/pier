import type { ReactNode } from "react";
import { Dialog, Heading, Modal, ModalOverlay } from "react-aria-components";

/** A modal dialog over the window. */
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
      className="fixed inset-0 z-50 flex items-start justify-center bg-black/40 pt-[12vh] backdrop-blur-[1px]"
    >
      <Modal
        style={{ width }}
        className="pop-in max-h-[76vh] overflow-auto rounded-[12px] border border-strong bg-popover shadow-2xl outline-none"
      >
        <Dialog className="outline-none">
          <Heading slot="title" className="px-5 pt-5 font-display text-md font-semibold">
            {title}
          </Heading>
          <div className="px-5 pt-3 pb-5">{children}</div>
        </Dialog>
      </Modal>
    </ModalOverlay>
  );
}
