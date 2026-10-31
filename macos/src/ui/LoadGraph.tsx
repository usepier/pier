/** The smallest 1, 2 or 5 × 10ⁿ at or above v: a graph's ceiling. */
function nice(v: number) {
  const p = 10 ** Math.floor(Math.log10(v));
  return ([1, 2, 5, 10].find((m) => m * p >= v) ?? 10) * p;
}

/**
 * A graph of readings over the last few minutes, placed by time: it fills
 * the width it's given, and moves left as readings come. Its ceiling sits
 * just above the highest reading, between min and max, so small changes
 * show without a quiet session looking busy.
 */
export function LoadGraph({
  points,
  window,
  min,
  max,
  height = 28,
  label,
  className = "text-accent",
}: {
  points: { t: number; v: number }[];
  window: number;
  min: number;
  max: number;
  height?: number;
  label: string;
  className?: string;
}) {
  const W = 300;
  const now = Date.now();
  const peak = Math.max(0, ...points.map((p) => p.v));
  const top = Math.min(max, Math.max(min, nice(peak * 1.15 || min)));
  const x = (t: number) => Math.max(0, ((t - (now - window)) / window) * W);
  const y = (v: number) => height - 1 - (Math.min(v, top) / top) * (height - 3);
  const d = points.map((p, i) => `${i ? "L" : "M"}${x(p.t).toFixed(1)},${y(p.v).toFixed(1)}`).join("");
  const first = points[0];
  const last = points[points.length - 1];
  return (
    <svg
      role="img"
      aria-label={label}
      viewBox={`0 0 ${W} ${height}`}
      preserveAspectRatio="none"
      height={height}
      className={`block w-full min-w-0 ${className}`}
    >
      <line x1={0} x2={W} y1={height - 0.5} y2={height - 0.5} stroke="currentColor" opacity="0.18" />
      {first && last && points.length > 1 && (
        <>
          <path
            d={`${d}L${x(last.t).toFixed(1)},${height}L${x(first.t).toFixed(1)},${height}Z`}
            fill="currentColor"
            opacity="0.14"
          />
          <path
            d={d}
            fill="none"
            stroke="currentColor"
            strokeWidth="1.5"
            strokeLinejoin="round"
            vectorEffect="non-scaling-stroke"
          />
        </>
      )}
    </svg>
  );
}
