import { useFormContext } from "react-hook-form";
import Card from "../../components/Card";
import FormGroup from "../../components/FormGroup";
import type { SettingsFormValues } from "./settings-schema";

export default function GeneralSettings() {
  const {
    register,
    formState: { errors },
  } = useFormContext<SettingsFormValues>();

  return (
    <div>
      <Card title="Download Path">
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
      </Card>
    </div>
  );
}
