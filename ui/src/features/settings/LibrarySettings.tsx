import { useFormContext } from "react-hook-form";
import type { ConfigUpdatePayload } from "../../api/types";
import Card from "../../components/Card";
import FormGroup from "../../components/FormGroup";
import Button from "../../components/Button";
import {
  toLibraryPayload,
  type SettingsFormValues,
} from "./settings-schema";

interface Props {
  onSave: (payload: ConfigUpdatePayload, section: string) => void;
  isSaving: boolean;
}

export default function LibrarySettings({ onSave, isSaving }: Props) {
  const {
    register,
    handleSubmit,
    formState: { errors },
  } = useFormContext<SettingsFormValues>();

  const onSaveLibraryPath = (values: SettingsFormValues) => {
    onSave(
      { library: { library_path: values.library_path ?? "" } },
      "Library path",
    );
  };

  const onSaveOrganization = (values: SettingsFormValues) => {
    onSave(toLibraryPayload(values), "Library organization");
  };

  const onSavePlaylists = (values: SettingsFormValues) => {
    onSave(toLibraryPayload(values), "Playlist settings");
  };

  return (
    <div>
      {/* Library Path */}
      <Card title="Library Path">
        <form onSubmit={handleSubmit(onSaveLibraryPath)}>
          <FormGroup
            label="Library Path"
            htmlFor="library_path"
            hint="Directory where your organized music library is stored."
            error={errors.library_path?.message}
          >
            <input
              id="library_path"
              type="text"
              placeholder="/data/library"
              {...register("library_path")}
              className="w-full rounded-lg border border-slate-700 bg-slate-800 px-3 py-2 text-sm text-white placeholder:text-slate-500 focus:border-purple-500 focus:outline-none focus:ring-1 focus:ring-purple-500"
            />
          </FormGroup>

          <div className="mt-4">
            <Button type="submit" loading={isSaving}>
              Save
            </Button>
          </div>
        </form>
      </Card>

      {/* Folder Organization */}
      <Card title="Folder Organization">
        <form onSubmit={handleSubmit(onSaveOrganization)}>
          <FormGroup
            label="Folder Template"
            htmlFor="folder_template"
            hint={
              <span>
                Available tokens:{" "}
                <code className="rounded bg-slate-800 px-1 py-0.5 text-xs text-purple-400">
                  {"{artist}"}
                </code>{" "}
                <code className="rounded bg-slate-800 px-1 py-0.5 text-xs text-purple-400">
                  {"{album}"}
                </code>{" "}
                <code className="rounded bg-slate-800 px-1 py-0.5 text-xs text-purple-400">
                  {"{year}"}
                </code>{" "}
                <code className="rounded bg-slate-800 px-1 py-0.5 text-xs text-purple-400">
                  {"{track:00}"}
                </code>{" "}
                <code className="rounded bg-slate-800 px-1 py-0.5 text-xs text-purple-400">
                  {"{title}"}
                </code>{" "}
                <code className="rounded bg-slate-800 px-1 py-0.5 text-xs text-purple-400">
                  {"{ext}"}
                </code>{" "}
                <code className="rounded bg-slate-800 px-1 py-0.5 text-xs text-purple-400">
                  {"{disc:00}"}
                </code>{" "}
                <code className="rounded bg-slate-800 px-1 py-0.5 text-xs text-purple-400">
                  {"{album_type}"}
                </code>
              </span>
            }
            error={errors.folder_template?.message}
          >
            <input
              id="folder_template"
              type="text"
              placeholder="{artist}/{album} ({year})/{track:00} - {title}.{ext}"
              {...register("folder_template")}
              className="w-full rounded-lg border border-slate-700 bg-slate-800 px-3 py-2 text-sm text-white placeholder:text-slate-500 focus:border-purple-500 focus:outline-none focus:ring-1 focus:ring-purple-500"
            />
          </FormGroup>

          <div className="mt-4">
            <Button type="submit" loading={isSaving}>
              Save
            </Button>
          </div>
        </form>
      </Card>

      {/* Playlist Download */}
      <Card title="Playlist Download">
        <form onSubmit={handleSubmit(onSavePlaylists)}>
          <div className="space-y-4">
            <FormGroup
              label="Playlist Directory"
              htmlFor="playlist_path"
              hint="Directory where downloaded playlists are stored."
              error={errors.playlist_path?.message}
            >
              <input
                id="playlist_path"
                type="text"
                placeholder="/data/playlists"
                {...register("playlist_path")}
                className="w-full rounded-lg border border-slate-700 bg-slate-800 px-3 py-2 text-sm text-white placeholder:text-slate-500 focus:border-purple-500 focus:outline-none focus:ring-1 focus:ring-purple-500"
              />
            </FormGroup>

            <FormGroup
              label="Playlist Template"
              htmlFor="playlist_template"
              hint={
                <span>
                  Available tokens:{" "}
                  <code className="rounded bg-slate-800 px-1 py-0.5 text-xs text-purple-400">
                    {"{position:02d}"}
                  </code>{" "}
                  <code className="rounded bg-slate-800 px-1 py-0.5 text-xs text-purple-400">
                    {"{artist}"}
                  </code>{" "}
                  <code className="rounded bg-slate-800 px-1 py-0.5 text-xs text-purple-400">
                    {"{title}"}
                  </code>{" "}
                  <code className="rounded bg-slate-800 px-1 py-0.5 text-xs text-purple-400">
                    {"{album}"}
                  </code>{" "}
                  <code className="rounded bg-slate-800 px-1 py-0.5 text-xs text-purple-400">
                    {"{ext}"}
                  </code>
                </span>
              }
              error={errors.playlist_template?.message}
            >
              <input
                id="playlist_template"
                type="text"
                placeholder="{position:02d} - {artist} - {title}.{ext}"
                {...register("playlist_template")}
                className="w-full rounded-lg border border-slate-700 bg-slate-800 px-3 py-2 text-sm text-white placeholder:text-slate-500 focus:border-purple-500 focus:outline-none focus:ring-1 focus:ring-purple-500"
              />
            </FormGroup>
          </div>

          <div className="mt-4">
            <Button type="submit" loading={isSaving}>
              Save
            </Button>
          </div>
        </form>
      </Card>
    </div>
  );
}
