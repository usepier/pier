/** A tiny trend line for the last few dozen readings. */
export function Sparkline({
  values,
  max,
  width = 96,
  height = 22,
  className = "text-accent",
}: {
  values: number[];
  max?: number;
  width?: number;
  height?: number;
  className?: string;
}) {
  if (values.length < 2) return <svg width={width} height={height} aria-hidden="true" />;
  const top = max ?? Math.max(1, ...values);
  const step = width / (values.length - 1);
  const y = (v: number) => height - 1 - (Math.min(v, top) / top) * (height - 2);
  const d = values.map((v, i) => `${i ? "L" : "M"}${(i * step).toFixed(1)},${y(v).toFixed(1)}`).join("");
  return (
    <svg width={width} height={height} className={className} aria-hidden="true">
      <path d={`${d}L${width},${height}L0,${height}Z`} fill="currentColor" opacity="0.12" />
      <path d={d} fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinejoin="round" />
    </svg>
  );
}
