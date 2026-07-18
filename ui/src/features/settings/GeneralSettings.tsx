import { useFormContext } from "react-hook-form";
import type { ConfigUpdatePayload } from "../../api/types";
import Card from "../../components/Card";
import FormGroup from "../../components/FormGroup";
import Button from "../../components/Button";
import { toGeneralPayload, type SettingsFormValues } from "./settings-schema";

interface Props {
  onSave: (payload: ConfigUpdatePayload, section: string) => void;
  isSaving: boolean;
}

export default function GeneralSettings({ onSave, isSaving }: Props) {
  const {
    register,
    handleSubmit,
    formState: { errors },
  } = useFormContext<SettingsFormValues>();

  const onSubmit = (values: SettingsFormValues) => {
    onSave(toGeneralPayload(values), "General");
  };

  return (
    <div>
      <Card title="Download Path">
        <form onSubmit={handleSubmit(onSubmit)}>
          <FormGroup
            label="Download Path"
            htmlFor="download_path"
            hint="Directory where new downloads are stored temporarily before library import."
            error={errors.download_path?.message}
          >
            <input
              id="download_path"
              type="text"
              placeholder="/data/downloads"
              {...register("download_path")}
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
    </div>
  );
}
