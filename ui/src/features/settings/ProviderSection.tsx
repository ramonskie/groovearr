import { useState, useCallback } from "react";
import { useFormContext, get } from "react-hook-form";
import { useTestConnection } from "../../hooks/use-config";
import type { SourceInfo, ConfigField } from "../../api/types";
import Card from "../../components/Card";
import FormGroup from "../../components/FormGroup";
import Button from "../../components/Button";
import Spinner from "../../components/Spinner";
import CapabilityBadges from "./CapabilityBadges";

interface Props {
  source: SourceInfo;
}

interface TestResult {
  ok: boolean;
  message: string;
}

export default function ProviderSection({ source }: Props) {
  const {
    register,
    watch,
    setValue,
    formState: { errors },
  } = useFormContext();

  const testConnection = useTestConnection();
  const [testResult, setTestResult] = useState<TestResult | null>(null);

  const formPrefix = `sources.${source.name}`;

  const handleTest = useCallback(async () => {
    setTestResult(null);
    try {
      await testConnection.mutateAsync(source.name);
      setTestResult({ ok: true, message: "Connected" });
    } catch (err) {
      setTestResult({
        ok: false,
        message: err instanceof Error ? err.message : "Connection failed",
      });
    }
  }, [testConnection, source.name]);

  const fields = source.config_schema ?? [];

  // Check if a dependent field should be visible.
  const isVisible = (field: ConfigField): boolean => {
    if (!field.depends_on) return true;
    const parentValue = watch(`${formPrefix}.${field.depends_on.field}`);
    return parentValue === field.depends_on.value;
  };

  const fieldError = (fieldName: string): string | undefined => {
    const path = `${formPrefix}.${fieldName}`;
    return get(errors, path)?.message as string | undefined;
  };

  const inputClass =
    "w-full rounded-lg border border-slate-700 bg-slate-800 px-3 py-2 text-sm text-white placeholder:text-slate-500 focus:border-purple-500 focus:outline-none focus:ring-1 focus:ring-purple-500";

  return (
    <Card
      title={source.display_name}
      actions={<CapabilityBadges capabilities={source.capabilities} />}
    >
      <div className="space-y-4">
        {fields.filter(isVisible).map((field) => (
          <FormGroup
            key={field.name}
            label={field.label}
            htmlFor={`${source.name}_${field.name}`}
            hint={field.hint}
            error={fieldError(field.name)}
          >
            {field.type === "toggle" ? (() => {
              const fieldPath = `${formPrefix}.${field.name}`;
              const raw = watch(fieldPath) as string | undefined;
              const enabled = raw === "true" || raw === undefined; // undefined = default true
              return (
                <button
                  type="button"
                  id={`${source.name}_${field.name}`}
                  onClick={() => setValue(fieldPath, enabled ? "false" : "true", { shouldDirty: true })}
                  className={`inline-flex items-center gap-2 rounded-full px-3 py-1 text-xs font-semibold transition-colors ${
                    enabled
                      ? "bg-green-600 text-white hover:bg-green-500"
                      : "bg-red-600 text-white hover:bg-red-500"
                  }`}
                >
                  <span className={`h-2 w-2 rounded-full ${enabled ? "bg-green-200" : "bg-red-200"}`} />
                  {enabled ? "ON" : "OFF"}
                </button>
              );
            })() : field.type === "select" ? (
              <select
                id={`${source.name}_${field.name}`}
                {...register(`${formPrefix}.${field.name}`)}
                className={inputClass}
              >
                {(field.options ?? []).map((opt) => (
                  <option key={opt.value} value={opt.value}>
                    {opt.label}
                  </option>
                ))}
              </select>
            ) : (
              <input
                id={`${source.name}_${field.name}`}
                type={field.type === "password" ? "password" : "text"}
                placeholder={field.placeholder}
                {...register(`${formPrefix}.${field.name}`)}
                className={inputClass}
              />
            )}
          </FormGroup>
        ))}
      </div>

      {testResult && (
        <div
          className={`mt-3 flex items-center gap-2 rounded-lg border p-3 text-sm ${
            testResult.ok
              ? "border-green-800 bg-green-950/50 text-green-300"
              : "border-red-800 bg-red-950/50 text-red-300"
          }`}
        >
          {testResult.message}
        </div>
      )}

      <div className="mt-4 flex items-center gap-3">
        <Button
          type="button"
          variant="ghost"
          loading={testConnection.isPending}
          onClick={handleTest}
        >
          {testConnection.isPending ? (
            <>
              <Spinner size="sm" />
              Testing...
            </>
          ) : (
            "Test Connection"
          )}
        </Button>

        {source.oauth?.enabled && (() => {
          const oa = source.oauth!;
          // Check visibility dependency
          if (oa.depends_on) {
            const depValue = watch(`${formPrefix}.${oa.depends_on.field}`);
            if (depValue !== oa.depends_on.value) return null;
          }
          return (
            <a
              href={oa.connect_url}
              target="_blank"
              rel="noopener noreferrer"
              className="inline-flex items-center gap-1 rounded-lg border border-green-700 bg-green-950/50 px-3 py-2 text-sm text-green-400 hover:bg-green-900/50 transition-colors"
            >
              {oa.connect_label}
            </a>
          );
        })()}
      </div>
    </Card>
  );
}
