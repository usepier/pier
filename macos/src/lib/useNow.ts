import { useEffect, useState } from "react";

/** The time now, updated every `every` ms while mounted. */
export function useNow(every = 1000): number {
  const [now, setNow] = useState(Date.now);
  useEffect(() => {
    const t = setInterval(() => setNow(Date.now()), every);
    return () => clearInterval(t);
  }, [every]);
  return now;
}
