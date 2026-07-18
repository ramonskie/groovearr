import { create } from "zustand";

/** Lightweight config shape — only fields needed for quick UI lookups.
 *  Full config retrieval lives in react-query hooks. */
export interface ConfigCache {
  soulseek?: {
    slskd_url: string;
    api_key: string;
  };
  deezer?: {
    arl: string;
    quality: string;
  };
  library?: {
    download_path: string;
    library_path: string;
    folder_template: string;
    playlist_path: string;
    playlist_template: string;
  };
}

interface ConfigCacheState {
  /** Partial config cached for UI convenience (badges, quick field lookups) */
  currentConfig: ConfigCache;
  /** Map of source name → connected state (e.g. { soulseek: true, deezer: false }) */
  connectedSources: Record<string, boolean>;

  updateConfig: (patch: Partial<ConfigCache>) => void;
  setSourceConnected: (sourceName: string, connected: boolean) => void;
}

export const useConfigCacheStore = create<ConfigCacheState>()((set) => ({
  currentConfig: {},
  connectedSources: {},

  updateConfig: (patch) =>
    set((state) => ({
      currentConfig: { ...state.currentConfig, ...patch },
    })),

  setSourceConnected: (sourceName, connected) =>
    set((state) => ({
      connectedSources: { ...state.connectedSources, [sourceName]: connected },
    })),
}));
