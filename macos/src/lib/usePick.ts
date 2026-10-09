// The highlighted row of a list that filters as you type.
import { useState } from "react";

/**
 * The row picked in a list of matches for query: back at the top for each
 * new query, without an effect to put it there.
 */
export function usePick(query: unknown): [number, (next: number | ((prev: number) => number)) => void] {
  const [pick, setPick] = useState({ query, index: 0 });
  const index = Object.is(pick.query, query) ? pick.index : 0;
  return [index, (next) => setPick({ query, index: typeof next === "function" ? next(index) : next })];
}
