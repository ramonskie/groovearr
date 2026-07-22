import { useState, useCallback, useMemo } from "react";
import type {
  QualityProfile,
  QualityTarget,
  UpgradePolicy,
  SearchMode,
  QualityProfileCreatePayload,
  QualityProfileUpdatePayload,
} from "../../api/types";
import {
  useQualityProfiles,
  useCreateQualityProfile,
  useUpdateQualityProfile,
  useDeleteQualityProfile,
  useSetDefaultQualityProfile,
  useQualityPresets,
} from "../../hooks/use-quality-profiles";
import Card from "../../components/Card";
import Button from "../../components/Button";
import Spinner from "../../components/Spinner";

// ─── Constants ────────────────────────────────────────────────────

const FORMAT_OPTIONS = [
  { value: "flac", label: "FLAC" },
  { value: "mp3", label: "MP3" },
  { value: "aac", label: "AAC" },
  { value: "ogg", label: "OGG" },
  { value: "opus", label: "Opus" },
  { value: "wav", label: "WAV" },
  { value: "alac", label: "ALAC" },
] as const;

const BIT_DEPTH_OPTIONS = [
  { value: undefined, label: "Any" },
  { value: 16, label: "16-bit" },
  { value: 24, label: "24-bit" },
] as const;

const UPGRADE_POLICY_OPTIONS: { value: UpgradePolicy; label: string }[] = [
  { value: "acceptable", label: "Acceptable — any match is good enough" },
  {
    value: "until_cutoff",
    label: "Until Cutoff — upgrade until reaching cutoff tier",
  },
  { value: "until_top", label: "Until Top — upgrade until best available" },
];

const EMPTY_TARGET: QualityTarget = {
  label: "",
  format: "mp3",
  min_bitrate: undefined,
  min_sample_rate: undefined,
  min_bit_depth: undefined,
};

const FORM_INPUT_CLASS =
  "w-full rounded-lg border border-slate-700 bg-slate-800 px-3 py-2 text-sm text-white placeholder:text-slate-500 focus:border-purple-500 focus:outline-none focus:ring-1 focus:ring-purple-500";

const FORM_SELECT_CLASS =
  "w-full rounded-lg border border-slate-700 bg-slate-800 px-3 py-2 text-sm text-white focus:border-purple-500 focus:outline-none focus:ring-1 focus:ring-purple-500";

// ─── Helpers ──────────────────────────────────────────────────────

/** Map audio format to a badge color. */
function formatBadgeColor(format: string): string {
  switch (format.toLowerCase()) {
    case "flac":
      return "bg-purple-900/50 text-purple-400 border-purple-800";
    case "mp3":
      return "bg-yellow-900/50 text-yellow-400 border-yellow-800";
    case "aac":
      return "bg-green-900/50 text-green-400 border-green-800";
    case "ogg":
      return "bg-orange-900/50 text-orange-400 border-orange-800";
    case "opus":
      return "bg-indigo-900/50 text-indigo-400 border-indigo-800";
    case "wav":
      return "bg-cyan-900/50 text-cyan-400 border-cyan-800";
    case "alac":
      return "bg-pink-900/50 text-pink-400 border-pink-800";
    default:
      return "bg-slate-800 text-slate-400 border-slate-700";
  }
}

/** Build a short info string from a QualityTarget (bitrate / depth / sample rate). */
function targetInfo(t: QualityTarget): string {
  const parts: string[] = [];
  if (t.min_bitrate) parts.push(`${t.min_bitrate}kbps`);
  if (t.min_bit_depth) parts.push(`${t.min_bit_depth}-bit`);
  if (t.min_sample_rate) {
    const khz = t.min_sample_rate / 1000;
    parts.push(`${khz}kHz`);
  }
  return parts.join(" · ");
}

// ─── Component ────────────────────────────────────────────────────

export default function QualitySettings() {
  const { data: profiles, isLoading: loadingProfiles } = useQualityProfiles();
  const { data: presets } = useQualityPresets();
  const createProfile = useCreateQualityProfile();
  const updateProfile = useUpdateQualityProfile();
  const deleteProfile = useDeleteQualityProfile();
  const setDefaultProfile = useSetDefaultQualityProfile();

  // ─── selection state ──────────────────────────────────────────

  const [selectedId, setSelectedId] = useState<number | null>(null);
  const [isPreview, setIsPreview] = useState(false);

  // ─── editor state ─────────────────────────────────────────────

  const [profileName, setProfileName] = useState("");
  const [profileDescription, setProfileDescription] = useState("");
  const [rankedTargets, setRankedTargets] = useState<QualityTarget[]>([]);
  const [fallbackEnabled, setFallbackEnabled] = useState(true);
  const [upgradePolicy, setUpgradePolicy] = useState<UpgradePolicy>("acceptable");
  const [cutoffIndex, setCutoffIndex] = useState(0);
  const [searchMode, setSearchMode] = useState<SearchMode>("priority");
  const [rankByQuality, setRankByQuality] = useState(false);
  const [replaceLowerQuality, setReplaceLowerQuality] = useState(false);

  // ─── UI state ─────────────────────────────────────────────────

  const [showAddTarget, setShowAddTarget] = useState(false);
  const [showNewProfileInput, setShowNewProfileInput] = useState(false);
  const [newProfileName, setNewProfileName] = useState("");
  const [deleteConfirmId, setDeleteConfirmId] = useState<number | null>(null);
  const [defaultConfirmId, setDefaultConfirmId] = useState<number | null>(null);
  const [saveError, setSaveError] = useState<string | null>(null);

  // target editor form state
  const [newTarget, setNewTarget] = useState<QualityTarget>({ ...EMPTY_TARGET });

  // ─── derived ──────────────────────────────────────────────────

  const selectedProfile = useMemo(
    () => profiles?.find((p) => p.id === selectedId) ?? null,
    [profiles, selectedId],
  );

  // ─── helpers ──────────────────────────────────────────────────

  /** Load a profile into the editor (preview or edit). */
  const loadIntoEditor = useCallback(
    (profile: QualityProfile, preview: boolean) => {
      setSelectedId(profile.id);
      setIsPreview(preview);
      setProfileName(profile.name);
      setProfileDescription(profile.description);
      setRankedTargets([...profile.ranked_targets]);
      setFallbackEnabled(profile.fallback_enabled);
      setUpgradePolicy(profile.upgrade_policy);
      setCutoffIndex(profile.upgrade_cutoff_index);
      setSearchMode(profile.search_mode);
      setRankByQuality(profile.rank_candidates_by_quality);
      setReplaceLowerQuality(profile.replace_lower_quality);
    },
    [],
  );

  /** Reset editor to empty state. */
  const resetEditor = useCallback(() => {
    setSelectedId(null);
    setIsPreview(false);
    setProfileName("");
    setProfileDescription("");
    setRankedTargets([]);
    setFallbackEnabled(true);
    setUpgradePolicy("acceptable");
    setCutoffIndex(0);
    setSearchMode("priority");
    setRankByQuality(false);
    setReplaceLowerQuality(false);
    setSaveError(null);
  }, []);

  /** Apply a preset to the editor (preview mode). */
  const applyPreset = useCallback((key: string) => {
    if (!presets) return;
    const preset = presets[key];
    if (!preset) return;
    setSelectedId(null);
    setIsPreview(true);
    setProfileName(preset.name);
    setProfileDescription(preset.description);
    setRankedTargets([...preset.ranked_targets]);
    setFallbackEnabled(preset.fallback_enabled);
    setUpgradePolicy(preset.upgrade_policy);
    setCutoffIndex(preset.upgrade_cutoff_index);
    setSearchMode(preset.search_mode);
    setRankByQuality(preset.rank_candidates_by_quality);
    setReplaceLowerQuality(preset.replace_lower_quality);
    setSaveError(null);
  }, [presets]);

  // ─── target operations ────────────────────────────────────────

  const moveTarget = useCallback(
    (fromIndex: number, direction: "up" | "down") => {
      const toIndex = direction === "up" ? fromIndex - 1 : fromIndex + 1;
      if (toIndex < 0 || toIndex >= rankedTargets.length) return;
      const arr = [...rankedTargets];
      const [moved] = arr.splice(fromIndex, 1);
      arr.splice(toIndex, 0, moved);
      setRankedTargets(arr);
    },
    [rankedTargets],
  );

  const removeTarget = useCallback(
    (index: number) => {
      setRankedTargets((prev) => {
        const next = prev.filter((_, i) => i !== index);
        // clamp cutoff index
        if (cutoffIndex >= next.length && next.length > 0) {
          setCutoffIndex(next.length - 1);
        }
        return next;
      });
    },
    [cutoffIndex],
  );

  const addTarget = useCallback(() => {
    if (!newTarget.format && !newTarget.label.trim()) return;
    const label =
      newTarget.label.trim() ||
      [newTarget.format?.toUpperCase(), newTarget.min_bitrate ? `${newTarget.min_bitrate}kbps` : null]
        .filter(Boolean)
        .join(" ");
    setRankedTargets((prev) => [
      ...prev,
      { ...newTarget, label },
    ]);
    setNewTarget({ ...EMPTY_TARGET });
    setShowAddTarget(false);
  }, [newTarget]);

  // ─── save / create ────────────────────────────────────────────

  const handleSave = useCallback(async () => {
    setSaveError(null);
    if (!selectedId) return;

    const payload: QualityProfileUpdatePayload = {
      name: profileName || undefined,
      description: profileDescription || undefined,
      ranked_targets: rankedTargets,
      fallback_enabled: fallbackEnabled,
      search_mode: searchMode,
      rank_candidates_by_quality: rankByQuality,
      upgrade_policy: upgradePolicy,
      upgrade_cutoff_index: cutoffIndex,
      replace_lower_quality: replaceLowerQuality,
    };

    try {
      await updateProfile.mutateAsync({ id: selectedId, payload });
      setIsPreview(false);
    } catch (err) {
      setSaveError(err instanceof Error ? err.message : "Failed to save profile");
    }
  }, [
    selectedId,
    profileName,
    profileDescription,
    rankedTargets,
    fallbackEnabled,
    searchMode,
    rankByQuality,
    upgradePolicy,
    cutoffIndex,
    replaceLowerQuality,
    updateProfile,
  ]);

  const handleSaveAsNew = useCallback(async () => {
    setSaveError(null);
    const name = newProfileName.trim();
    if (!name) return;

    const payload: QualityProfileCreatePayload = {
      name,
      description: profileDescription || undefined,
      ranked_targets: rankedTargets,
      fallback_enabled: fallbackEnabled,
      search_mode: searchMode,
      rank_candidates_by_quality: rankByQuality,
      upgrade_policy: upgradePolicy,
      upgrade_cutoff_index: cutoffIndex,
      replace_lower_quality: replaceLowerQuality,
    };

    try {
      const created = await createProfile.mutateAsync(payload);
      setNewProfileName("");
      setShowNewProfileInput(false);
      loadIntoEditor(created, false);
    } catch (err) {
      setSaveError(err instanceof Error ? err.message : "Failed to create profile");
    }
  }, [
    newProfileName,
    profileDescription,
    rankedTargets,
    fallbackEnabled,
    searchMode,
    rankByQuality,
    upgradePolicy,
    cutoffIndex,
    replaceLowerQuality,
    createProfile,
    loadIntoEditor,
  ]);

  const handleDelete = useCallback(
    async (id: number) => {
      try {
        await deleteProfile.mutateAsync(id);
        if (selectedId === id) resetEditor();
      } catch {
        // error handled by mutation state
      }
      setDeleteConfirmId(null);
    },
    [deleteProfile, selectedId, resetEditor],
  );

  const handleSetDefault = useCallback(
    async (id: number) => {
      try {
        await setDefaultProfile.mutateAsync(id);
      } catch {
        // error handled by mutation state
      }
      setDefaultConfirmId(null);
    },
    [setDefaultProfile],
  );

  // ─── cutoff labels (derived from targets) ─────────────────────

  const cutoffLabels = useMemo(
    () => rankedTargets.map((t) => t.label),
    [rankedTargets],
  );

  // ─── render helpers ───────────────────────────────────────────

  const isSaving = updateProfile.isPending || createProfile.isPending;

  // ─── loading ──────────────────────────────────────────────────

  if (loadingProfiles) {
    return (
      <div className="flex items-center justify-center py-16">
        <Spinner size="lg" />
      </div>
    );
  }

  // ─── render ───────────────────────────────────────────────────

  return (
    <div className="flex flex-col gap-6 lg:flex-row">
      {/* ─── Left Sidebar: Profile List ─────────────────────── */}
      <aside className="w-full shrink-0 lg:w-64">
        <Card>
          {/* Presets */}
          <div className="space-y-3">
            <label className="text-xs font-medium uppercase tracking-wider text-slate-400">
              Presets
            </label>
            <div className="flex flex-wrap gap-1.5">
              {presets && Object.entries(presets).map(([key, preset]) => (
                <button
                  key={key}
                  type="button"
                  onClick={() => applyPreset(key)}
                  className="rounded border border-slate-700 bg-slate-800 px-2.5 py-1 text-xs font-medium text-slate-300 transition-colors hover:border-purple-500 hover:text-white"
                >
                  {preset.name}
                </button>
              ))}
            </div>
          </div>

          {/* Save as new */}
          <div className="mt-4 border-t border-slate-800 pt-3">
            {showNewProfileInput ? (
              <div className="flex gap-2">
                <input
                  type="text"
                  value={newProfileName}
                  onChange={(e) => setNewProfileName(e.target.value)}
                  placeholder="New profile name"
                  className={`flex-1 ${FORM_INPUT_CLASS}`}
                  onKeyDown={(e) => {
                    if (e.key === "Enter") handleSaveAsNew();
                    if (e.key === "Escape") {
                      setShowNewProfileInput(false);
                      setNewProfileName("");
                    }
                  }}
                  autoFocus
                />
                <Button size="sm" onClick={handleSaveAsNew} loading={isSaving}>
                  Save
                </Button>
                <Button
                  size="sm"
                  variant="ghost"
                  onClick={() => {
                    setShowNewProfileInput(false);
                    setNewProfileName("");
                  }}
                >
                  Cancel
                </Button>
              </div>
            ) : (
              <Button
                variant="ghost"
                size="sm"
                onClick={() => setShowNewProfileInput(true)}
                className="w-full"
              >
                ✚ Save Current as New
              </Button>
            )}
          </div>

          {/* Profile list */}
          <div className="mt-3 border-t border-slate-800 pt-3">
            <p className="mb-2 text-xs font-medium uppercase tracking-wider text-slate-400">
              Profiles
            </p>
            <p className="mb-2 text-[11px] leading-relaxed text-slate-500">
              <span className="text-purple-400">●</span> = active default &nbsp;
              <span className="text-slate-500">○</span> = click to set as default
            </p>
            {profiles && profiles.length === 0 ? (
              <p className="text-xs text-slate-500">No profiles yet.</p>
            ) : (
              <ul className="space-y-1">
                {profiles?.map((profile) => {
                  const isActive = selectedId === profile.id && !isPreview;
                  const isPreviewing = selectedId === profile.id && isPreview;
                  return (
                    <li
                      key={profile.id}
                      className={`group flex items-center gap-1.5 rounded px-2 py-1.5 text-sm transition-colors ${
                        isActive
                          ? "bg-purple-900/30 text-white"
                          : isPreviewing
                            ? "bg-yellow-900/20 text-yellow-200"
                            : "text-slate-300 hover:bg-slate-800/50"
                      }`}
                    >
                      {/* default indicator */}
                      <button
                        type="button"
                        title={
                          profile.is_default
                            ? "Current default — click to change"
                            : "Click to set as default"
                        }
                        onClick={() => setDefaultConfirmId(profile.id)}
                        className={`shrink-0 text-lg leading-none transition-colors hover:text-purple-400 ${
                          profile.is_default
                            ? "text-purple-400"
                            : "text-slate-500 hover:text-purple-400"
                        }`}
                      >
                        ●
                      </button>

                      {/* name + default badge */}
                      <button
                        type="button"
                        onClick={() => loadIntoEditor(profile, true)}
                        className="min-w-0 flex-1 text-left hover:text-white"
                      >
                        <span className={`truncate ${profile.is_default ? "font-semibold text-white" : ""}`}>
                          {profile.name}
                        </span>
                        {profile.is_default && (
                          <span className="ml-1.5 rounded bg-purple-900/40 px-1.5 py-0.5 text-[10px] font-medium uppercase tracking-wider text-purple-300">
                            Default
                          </span>
                        )}
                      </button>

                      {/* actions */}
                      <span className="flex items-center gap-0.5 opacity-0 transition-opacity group-hover:opacity-100">
                        <button
                          type="button"
                          title="Edit"
                          onClick={() => loadIntoEditor(profile, false)}
                          className="rounded p-0.5 text-slate-500 hover:text-white"
                        >
                          ✎
                        </button>
                        <button
                          type="button"
                          title="Delete"
                          onClick={() => setDeleteConfirmId(profile.id)}
                          className="rounded p-0.5 text-slate-500 hover:text-red-400"
                        >
                          🗑
                        </button>
                      </span>
                    </li>
                  );
                })}
              </ul>
                )}
              </div>
            </Card>

        {/* ─── Delete confirmation ────────────────────────── */}
        {deleteConfirmId !== null && (
          <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/60">
            <div className="w-full max-w-sm rounded-lg border border-slate-700 bg-slate-900 p-5">
              <h3 className="text-sm font-semibold text-white">
                Delete Profile?
              </h3>
              <p className="mt-2 text-sm text-slate-400">
                Tracks using this profile will revert to the default. This cannot
                be undone.
              </p>
              <div className="mt-4 flex justify-end gap-3">
                <Button
                  variant="ghost"
                  size="sm"
                  onClick={() => setDeleteConfirmId(null)}
                >
                  Cancel
                </Button>
                <Button
                  variant="danger"
                  size="sm"
                  loading={deleteProfile.isPending}
                  onClick={() => handleDelete(deleteConfirmId)}
                >
                  Delete
                </Button>
              </div>
            </div>
          </div>
        )}

        {/* ─── Set-Default confirmation ──────────────────── */}
        {defaultConfirmId !== null && (
          <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/60">
            <div className="w-full max-w-sm rounded-lg border border-slate-700 bg-slate-900 p-5">
              <h3 className="text-sm font-semibold text-white">
                Set as Default?
              </h3>
              <p className="mt-2 text-sm text-slate-400">
                This profile will become the app-wide default for all new
                downloads.
              </p>
              <div className="mt-4 flex justify-end gap-3">
                <Button
                  variant="ghost"
                  size="sm"
                  onClick={() => setDefaultConfirmId(null)}
                >
                  Cancel
                </Button>
                <Button
                  variant="primary"
                  size="sm"
                  loading={setDefaultProfile.isPending}
                  onClick={() => handleSetDefault(defaultConfirmId)}
                >
                  Set Default
                </Button>
              </div>
            </div>
          </div>
        )}
      </aside>

      {/* ─── Right Panel: Editor ────────────────────────────── */}
      <section className="min-w-0 flex-1 space-y-4">
        {/* Preview / empty banner */}
        {!selectedId && !isPreview && rankedTargets.length === 0 ? (
          <Card>
            <p className="py-8 text-center text-sm text-slate-500">
              Select a profile from the sidebar or choose a preset to get
              started.
            </p>
          </Card>
        ) : null}

        {(selectedId || isPreview) && (
          <>
            {/* Preview banner */}
            {isPreview && (
              <div className="flex items-center gap-2 rounded-lg border border-yellow-800 bg-yellow-950/30 px-4 py-2 text-sm text-yellow-300">
                <span>⚠</span>
                <span>
                  Previewing &ldquo;{selectedProfile?.name ?? profileName}
                  &rdquo; — edits will not be saved. Click{" "}
                  <button
                    type="button"
                    onClick={() => {
                      if (selectedProfile) loadIntoEditor(selectedProfile, false);
                      else setIsPreview(false);
                    }}
                    className="font-medium text-yellow-200 underline hover:text-white"
                  >
                    Edit
                  </button>{" "}
                  to enable saving.
                </span>
              </div>
            )}

            {saveError && (
              <div className="rounded-lg border border-red-800 bg-red-950/30 px-4 py-2 text-sm text-red-300">
                {saveError}
              </div>
            )}

            {/* Profile name */}
            {!isPreview && (
              <Card title="Profile Name">
                <div className="space-y-3">
                  <div>
                    <label
                      htmlFor="qp-name"
                      className="text-xs font-medium uppercase tracking-wider text-slate-400"
                    >
                      Name
                    </label>
                    <input
                      id="qp-name"
                      type="text"
                      value={profileName}
                      onChange={(e) => setProfileName(e.target.value)}
                      placeholder="My Quality Profile"
                      className={`mt-1 ${FORM_INPUT_CLASS}`}
                    />
                  </div>
                  <div>
                    <label
                      htmlFor="qp-desc"
                      className="text-xs font-medium uppercase tracking-wider text-slate-400"
                    >
                      Description
                    </label>
                    <input
                      id="qp-desc"
                      type="text"
                      value={profileDescription}
                      onChange={(e) => setProfileDescription(e.target.value)}
                      placeholder="Optional description"
                      className={`mt-1 ${FORM_INPUT_CLASS}`}
                    />
                  </div>
                </div>
              </Card>
            )}

            {/* Ranked Targets */}
            <Card title="Ranked Quality Targets">
              <p className="mb-3 text-xs text-slate-500">
                Higher-priority targets are listed first. Drag or use arrows to
                reorder.
              </p>

              <div className="flex flex-wrap gap-2">
                {rankedTargets.map((target, i) => (
                  <div
                    key={`${target.label}-${i}`}
                    className="flex items-center gap-1.5 rounded-lg border border-slate-700 bg-slate-800 px-3 py-2"
                  >
                    {/* reorder — up */}
                    <button
                      type="button"
                      title="Move up"
                      disabled={i === 0}
                      onClick={() => moveTarget(i, "up")}
                      className="text-xs text-slate-500 hover:text-white disabled:opacity-30"
                    >
                      ▲
                    </button>
                    {/* reorder — down */}
                    <button
                      type="button"
                      title="Move down"
                      disabled={i === rankedTargets.length - 1}
                      onClick={() => moveTarget(i, "down")}
                      className="text-xs text-slate-500 hover:text-white disabled:opacity-30"
                    >
                      ▼
                    </button>

                    {/* format badge */}
                    {target.format && (
                      <span
                        className={`inline-flex items-center rounded border px-2 py-0.5 text-xs font-semibold ${formatBadgeColor(target.format)}`}
                      >
                        {target.format.toUpperCase()}
                      </span>
                    )}

                    {/* label + info */}
                    <span className="text-sm text-white">{target.label}</span>
                    {targetInfo(target) && (
                      <span className="text-xs text-slate-500">
                        {targetInfo(target)}
                      </span>
                    )}

                    {/* remove */}
                    <button
                      type="button"
                      title="Remove target"
                      onClick={() => removeTarget(i)}
                      className="ml-1 text-sm text-slate-500 hover:text-red-400"
                    >
                      ×
                    </button>
                  </div>
                ))}

                {/* add target button / form */}
                {!showAddTarget ? (
                  <button
                    type="button"
                    onClick={() => {
                      setNewTarget({ ...EMPTY_TARGET });
                      setShowAddTarget(true);
                    }}
                    className="flex items-center gap-1 rounded-lg border border-dashed border-slate-600 px-3 py-2 text-sm text-slate-400 transition-colors hover:border-purple-500 hover:text-purple-400"
                  >
                    + Add Target
                  </button>
                ) : (
                  <div className="w-full rounded-lg border border-dashed border-purple-600 bg-slate-800/50 p-3">
                    <div className="grid grid-cols-2 gap-2 sm:grid-cols-4">
                      {/* format */}
                      <div>
                        <label className="text-xs font-medium text-slate-400">
                          Format
                        </label>
                        <select
                          value={newTarget.format ?? ""}
                          onChange={(e) =>
                            setNewTarget((prev) => ({
                              ...prev,
                              format: e.target.value || undefined,
                            }))
                          }
                          className={`mt-0.5 ${FORM_SELECT_CLASS} text-xs`}
                        >
                          {FORMAT_OPTIONS.map((f) => (
                            <option key={f.value} value={f.value}>
                              {f.label}
                            </option>
                          ))}
                        </select>
                      </div>

                      {/* min bitrate */}
                      <div>
                        <label className="text-xs font-medium text-slate-400">
                          Min Bitrate
                        </label>
                        <input
                          type="number"
                          placeholder="e.g. 320"
                          value={newTarget.min_bitrate ?? ""}
                          onChange={(e) =>
                            setNewTarget((prev) => ({
                              ...prev,
                              min_bitrate: e.target.value
                                ? Number(e.target.value)
                                : undefined,
                            }))
                          }
                          className={`mt-0.5 ${FORM_INPUT_CLASS} text-xs`}
                        />
                      </div>

                      {/* min sample rate */}
                      <div>
                        <label className="text-xs font-medium text-slate-400">
                          Min Sample Rate
                        </label>
                        <input
                          type="number"
                          placeholder="e.g. 44100"
                          value={newTarget.min_sample_rate ?? ""}
                          onChange={(e) =>
                            setNewTarget((prev) => ({
                              ...prev,
                              min_sample_rate: e.target.value
                                ? Number(e.target.value)
                                : undefined,
                            }))
                          }
                          className={`mt-0.5 ${FORM_INPUT_CLASS} text-xs`}
                        />
                      </div>

                      {/* min bit depth */}
                      <div>
                        <label className="text-xs font-medium text-slate-400">
                          Min Bit Depth
                        </label>
                        <select
                          value={newTarget.min_bit_depth ?? ""}
                          onChange={(e) =>
                            setNewTarget((prev) => ({
                              ...prev,
                              min_bit_depth: e.target.value
                                ? Number(e.target.value)
                                : undefined,
                            }))
                          }
                          className={`mt-0.5 ${FORM_SELECT_CLASS} text-xs`}
                        >
                          {BIT_DEPTH_OPTIONS.map((b) => (
                            <option
                              key={String(b.value ?? "")}
                              value={b.value ?? ""}
                            >
                              {b.label}
                            </option>
                          ))}
                        </select>
                      </div>
                    </div>

                    <div className="mt-2">
                      <label className="text-xs font-medium text-slate-400">
                        Label
                      </label>
                      <input
                        type="text"
                        placeholder="e.g. FLAC 16-bit"
                        value={newTarget.label}
                        onChange={(e) =>
                          setNewTarget((prev) => ({
                            ...prev,
                            label: e.target.value,
                          }))
                        }
                        className={`mt-0.5 ${FORM_INPUT_CLASS} text-xs`}
                        onKeyDown={(e) => {
                          if (e.key === "Enter") addTarget();
                        }}
                      />
                    </div>

                    <div className="mt-3 flex gap-2">
                      <Button size="sm" onClick={addTarget}>
                        Add
                      </Button>
                      <Button
                        size="sm"
                        variant="ghost"
                        onClick={() => {
                          setShowAddTarget(false);
                          setNewTarget({ ...EMPTY_TARGET });
                        }}
                      >
                        Cancel
                      </Button>
                    </div>
                  </div>
                )}
              </div>
            </Card>

            {/* Fallback */}
            <Card>
              <label className="flex cursor-pointer items-center gap-3">
                <input
                  type="checkbox"
                  checked={fallbackEnabled}
                  onChange={(e) => setFallbackEnabled(e.target.checked)}
                  className="h-4 w-4 rounded border-slate-700 bg-slate-800 text-purple-500 focus:ring-purple-500"
                />
                <div>
                  <span className="text-sm font-medium text-white">
                    Accept fallback
                  </span>
                  <p className="mt-0.5 text-xs text-slate-500">
                    Accept files that are below the lowest-ranked target when no
                    better match is found.
                  </p>
                </div>
              </label>
            </Card>

            {/* Upgrade */}
            <Card title="Upgrade">
              <div className="space-y-4">
                {/* upgrade policy */}
                <div>
                  <label
                    htmlFor="qp-policy"
                    className="text-xs font-medium uppercase tracking-wider text-slate-400"
                  >
                    Upgrade Policy
                  </label>
                  <select
                    id="qp-policy"
                    value={upgradePolicy}
                    onChange={(e) =>
                      setUpgradePolicy(e.target.value as UpgradePolicy)
                    }
                    className={`mt-1 ${FORM_SELECT_CLASS}`}
                  >
                    {UPGRADE_POLICY_OPTIONS.map((p) => (
                      <option key={p.value} value={p.value}>
                        {p.label}
                      </option>
                    ))}
                  </select>
                </div>

                {/* cutoff index (when policy !== acceptable) */}
                {upgradePolicy !== "acceptable" && (
                  <div>
                    <label
                      htmlFor="qp-cutoff"
                      className="text-xs font-medium uppercase tracking-wider text-slate-400"
                    >
                      Cutoff Target
                    </label>
                    <select
                      id="qp-cutoff"
                      value={cutoffIndex}
                      onChange={(e) => setCutoffIndex(Number(e.target.value))}
                      className={`mt-1 ${FORM_SELECT_CLASS}`}
                    >
                      {cutoffLabels.length > 0 ? (
                        cutoffLabels.map((label, i) => (
                          <option key={i} value={i}>
                            {label}
                            {i === 0 ? " (best)" : ""}
                          </option>
                        ))
                      ) : (
                        <option value={0}>— no targets defined —</option>
                      )}
                    </select>
                    <p className="mt-1 text-xs text-slate-500">
                      {upgradePolicy === "until_top"
                        ? "Keep upgrading until the track reaches this target (defaults to best)."
                        : "Once a track meets this target, stop upgrading."}
                    </p>
                  </div>
                )}

                {/* search mode */}
                <div>
                  <label
                    htmlFor="qp-search-mode"
                    className="text-xs font-medium uppercase tracking-wider text-slate-400"
                  >
                    Search Mode
                  </label>
                  <select
                    id="qp-search-mode"
                    value={searchMode}
                    onChange={(e) =>
                      setSearchMode(e.target.value as SearchMode)
                    }
                    className={`mt-1 ${FORM_SELECT_CLASS}`}
                  >
                    <option value="priority">
                      Priority — prefer best match within quality group
                    </option>
                    <option value="best_quality">
                      Best Quality — pool all sources, quality first
                    </option>
                  </select>
                </div>

                {/* rank by quality */}
                <div className="flex items-center justify-between">
                  <label
                    htmlFor="qp-rank-by-quality"
                    className="text-xs font-medium uppercase tracking-wider text-slate-400"
                  >
                    Rank by Audio Quality
                  </label>
                  <button
                    id="qp-rank-by-quality"
                    type="button"
                    onClick={() => setRankByQuality(!rankByQuality)}
                    className={`rounded-full px-3 py-1 text-xs font-medium transition-colors ${
                      rankByQuality
                        ? "bg-purple-600 text-white"
                        : "bg-slate-700 text-slate-400"
                    }`}
                  >
                    {rankByQuality ? "On" : "Off"}
                  </button>
                </div>

                {/* replace lower quality */}
                <div className="flex items-center justify-between">
                  <label
                    htmlFor="qp-replace-lower"
                    className="text-xs font-medium uppercase tracking-wider text-slate-400"
                  >
                    Replace Lower Quality on Import
                  </label>
                  <button
                    id="qp-replace-lower"
                    type="button"
                    onClick={() => setReplaceLowerQuality(!replaceLowerQuality)}
                    className={`rounded-full px-3 py-1 text-xs font-medium transition-colors ${
                      replaceLowerQuality
                        ? "bg-purple-600 text-white"
                        : "bg-slate-700 text-slate-400"
                    }`}
                  >
                    {replaceLowerQuality ? "On" : "Off"}
                  </button>
                </div>
              </div>
            </Card>

            {/* Save button */}
            {!isPreview && selectedId && (
              <div className="flex items-center gap-3">
                <Button
                  onClick={handleSave}
                  loading={isSaving}
                  disabled={!profileName.trim() || rankedTargets.length === 0}
                >
                  {isSaving ? "Saving..." : "Save Profile"}
                </Button>
                <Button variant="ghost" onClick={resetEditor}>
                  Cancel
                </Button>
              </div>
            )}
          </>
        )}
      </section>
    </div>
  );
}
