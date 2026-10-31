import type { ReactNode } from "react";

/** A centered, quiet placeholder for views with nothing to show yet. */
export function Empty({
  icon,
  title,
  detail,
  children,
}: {
  icon?: ReactNode;
  title: string;
  detail?: ReactNode;
  children?: ReactNode;
}) {
  return (
    <div className="flex h-full flex-col items-center justify-center gap-2 p-8 text-center">
      {icon && <div className="text-tertiary">{icon}</div>}
      <div className="text-md font-semibold text-label">{title}</div>
      {detail && <div className="max-w-sm text-sm text-secondary">{detail}</div>}
      {children && <div className="mt-2 flex gap-2">{children}</div>}
    </div>
  );
}
