import { create } from "zustand";

interface DownloadPollState {
  /** Whether the download list is actively being polled */
  pollingActive: boolean;
  /** Number of downloads currently in progress */
  activeCount: number;

  startPolling: () => void;
  stopPolling: () => void;
  setActiveCount: (count: number) => void;
}

export const useDownloadPollStore = create<DownloadPollState>()((set) => ({
  pollingActive: false,
  activeCount: 0,

  startPolling: () => set({ pollingActive: true }),
  stopPolling: () => set({ pollingActive: false }),
  setActiveCount: (count) => set({ activeCount: count }),
}));
