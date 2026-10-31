import { Switch as AriaSwitch } from "react-aria-components";

/** An on/off switch. */
export function Switch({
  isSelected,
  onChange,
  label,
  isDisabled,
}: {
  isSelected: boolean;
  onChange: (on: boolean) => void;
  label: string;
  isDisabled?: boolean;
}) {
  return (
    <AriaSwitch
      isSelected={isSelected}
      onChange={onChange}
      isDisabled={isDisabled}
      aria-label={label}
      className="group inline-flex cursor-pointer items-center outline-none disabled:cursor-default data-disabled:opacity-40"
    >
      <span className="flex h-[20px] w-[36px] items-center rounded-full border border-strong bg-selected px-[2px] transition-colors duration-150 group-hover:border-quaternary group-data-selected:border-transparent group-data-selected:bg-accent group-data-focus-visible:outline-2 group-data-focus-visible:outline-offset-2 group-data-focus-visible:outline-accent">
        <span className="size-[14px] rounded-full bg-secondary shadow-sm transition-transform duration-150 group-data-selected:translate-x-[16px] group-data-selected:bg-white" />
      </span>
    </AriaSwitch>
  );
}
