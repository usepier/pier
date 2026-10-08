// Background jobs (creates, bakes, fills, resizes) the app started or found
// running, and live progress for each.
import { useEffect } from "react";
import { create } from "zustand";
import { rpc } from "@/api/client";
import { useStatus, useWatch } from "@/api/live";
import type { Job, JobUpdate } from "@/api/protocol.gen";

interface Jobs {
  jobs: Record<string, Job>;
  upsert: (j: Job) => void;
}

export const useJobs = create<Jobs>((set) => ({
  jobs: {},
  upsert: (j) => set((s) => ({ jobs: { ...s.jobs, [j.id]: j } })),
}));

export function trackJob(j: Job) {
  useJobs.getState().upsert(j);
}

/** Loads the jobs still running from before the app (re)started. */
export function useJobsBootstrap() {
  const status = useStatus();
  useEffect(() => {
    if (status !== "ready") return;
    void rpc.call("jobs.list", {}).then((list) => {
      for (const j of list) if (j.state === "running") trackJob(j);
    });
  }, [status]);
}

/** A job with all its progress events so far, live. */
export function useJob(id: string | undefined): JobUpdate | undefined {
  const live = useWatch(
    "job",
    { id: id ?? "" },
    {
      enabled: !!id,
      reduce: (prev, next) => (prev ? { job: next.job, events: [...prev.events, ...next.events] } : next),
    },
  );
  const upsert = useJobs((s) => s.upsert);
  useEffect(() => {
    if (live.data) upsert(live.data.job);
  }, [live.data, upsert]);
  return id ? live.data : undefined;
}

export function useRunningJobs(filter?: (j: Job) => boolean): Job[] {
  const jobs = useJobs((s) => s.jobs);
  return Object.values(jobs).filter((j) => j.state === "running" && (!filter || filter(j)));
}
