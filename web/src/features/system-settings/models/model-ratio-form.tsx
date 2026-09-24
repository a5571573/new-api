/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { useQuery } from "@tanstack/react-query";
import { Code2, Download, Eye, RotateCcw, Save, Upload } from "lucide-react";
import { memo, useCallback, useEffect, useRef, useState } from "react";
import type { UseFormReturn } from "react-hook-form";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";

import { JsonCodeEditor } from "@/components/json-code-editor";
import { Button } from "@/components/ui/button";
import {
  Form,
  FormControl,
  FormDescription,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from "@/components/ui/form";
import { Switch } from "@/components/ui/switch";
import { getEnabledModels } from "@/features/channels/api";

import {
  SettingsForm,
  SettingsSwitchContent,
  SettingsSwitchItem,
} from "../components/settings-form-layout";
import {
  ModelRatioVisualEditor,
  type ModelRatioVisualEditorHandle,
} from "./model-ratio-visual-editor";

import { api, getFreshAuthHeaders } from "@/lib/api";

type ModelFormValues = {
  ModelPrice: string;
  ModelRatio: string;
  CacheRatio: string;
  CreateCacheRatio: string;
  CompletionRatio: string;
  ImageRatio: string;
  AudioRatio: string;
  AudioCompletionRatio: string;
  ExposeRatioEnabled: boolean;
  BillingMode: string;
  BillingExpr: string;
};

type ModelRatioFormProps = {
  form: UseFormReturn<ModelFormValues>;
  savedValues: ModelFormValues;
  onSave: (values: ModelFormValues) => Promise<void>;
  onReset: () => void;
  isSaving: boolean;
  isResetting: boolean;
  variant?: "default" | "unset";
};

type ModelJsonFieldName =
  | "ModelPrice"
  | "ModelRatio"
  | "CacheRatio"
  | "CreateCacheRatio"
  | "CompletionRatio"
  | "ImageRatio"
  | "AudioRatio"
  | "AudioCompletionRatio";

const modelJsonFields: Array<{
  name: ModelJsonFieldName;
  labelKey: string;
  descriptionKey: string;
}> = [
  {
    name: "ModelPrice",
    labelKey: "Model fixed pricing",
    descriptionKey:
      "JSON map of model → USD cost per request. Takes precedence over ratio based billing.",
  },
  {
    name: "ModelRatio",
    labelKey: "Model ratio",
    descriptionKey: "JSON map of model → multiplier applied to quota billing.",
  },
  {
    name: "CacheRatio",
    labelKey: "Prompt cache ratio",
    descriptionKey: "Optional ratio used when upstream cache hits occur.",
  },
  {
    name: "CreateCacheRatio",
    labelKey: "Create cache ratio",
    descriptionKey:
      "Ratio applied when creating cache entries for supported models.",
  },
  {
    name: "CompletionRatio",
    labelKey: "Completion ratio",
    descriptionKey:
      "Applies to custom completion endpoints. JSON map of model → ratio.",
  },
  {
    name: "ImageRatio",
    labelKey: "Image ratio",
    descriptionKey: "Configure per-model ratio for image inputs or outputs.",
  },
  {
    name: "AudioRatio",
    labelKey: "Audio ratio",
    descriptionKey:
      "Ratio applied to audio inputs where supported by the upstream model.",
  },
  {
    name: "AudioCompletionRatio",
    labelKey: "Audio completion ratio",
    descriptionKey: "Ratio applied to audio completions for streaming models.",
  },
];

function ModelJsonTextareaField(props: {
  form: UseFormReturn<ModelFormValues>;
  name: ModelJsonFieldName;
  label: string;
  description: string;
}) {
  return (
    <FormField
      control={props.form.control}
      name={props.name}
      render={({ field }) => (
        <FormItem className="flex min-w-0 flex-col gap-2">
          <FormLabel>{props.label}</FormLabel>
          <FormControl>
            <JsonCodeEditor
              value={field.value}
              onChange={(value) => field.onChange(value)}
              name={field.name}
              onBlur={field.onBlur}
              textareaRef={field.ref}
            />
          </FormControl>
          <FormDescription className="text-xs leading-5">
            {props.description}
          </FormDescription>
          <FormMessage />
        </FormItem>
      )}
    />
  );
}

export const ModelRatioForm = memo(function ModelRatioForm({
  form,
  savedValues,
  onSave,
  onReset,
  isSaving,
  isResetting,
  variant = "default",
}: ModelRatioFormProps) {
  const { t } = useTranslation();
  const isUnsetVariant = variant === "unset";
  const [editMode, setEditMode] = useState<"visual" | "json">("visual");
  const visualEditorRef = useRef<ModelRatioVisualEditorHandle>(null);

  // 隱藏的 Input Ref (用於觸發上傳)
  const fileInputRef = useRef<HTMLInputElement>(null);
  const [isUploading, setIsUploading] = useState(false);

  const enabledModelsQuery = useQuery({
    queryKey: ["enabled-models"],
    queryFn: getEnabledModels,
    enabled: isUnsetVariant,
  });

  const enabledModelsError = isUnsetVariant
    ? enabledModelsQuery.isError ||
      (enabledModelsQuery.data !== undefined &&
        !enabledModelsQuery.data.success)
    : false;
  const enabledModelsErrorMessage = enabledModelsQuery.data?.message;

  useEffect(() => {
    if (!enabledModelsError) return;
    toast.error(
      enabledModelsErrorMessage || t("Failed to load enabled models")
    );
  }, [enabledModelsError, enabledModelsErrorMessage, t]);

  const handleFieldChange = useCallback(
    (field: keyof ModelFormValues, value: string) => {
      form.setValue(field, value, {
        shouldValidate: true,
        shouldDirty: true,
      });
    },
    [form]
  );

  const toggleEditMode = useCallback(() => {
    setEditMode((prev) => (prev === "visual" ? "json" : "visual"));
  }, []);

  const handleSave = useCallback(async () => {
    if (editMode === "visual") {
      const committed = await visualEditorRef.current?.commitOpenEditor();
      if (committed === false) return;
    }

    await form.handleSubmit(onSave)();
  }, [editMode, form, onSave]);

  // --- 匯出 Excel 邏輯 ---
  const handleExportExcel = useCallback(async () => {
    try {
      const headers = await getFreshAuthHeaders();
      const res = await api.get("/api/option/export_model_ratios", {
        headers, // 關鍵：顯式注入 Root 管理員憑證
        responseType: "blob",
      });

      const blob = res.data instanceof Blob ? res.data : new Blob([res.data]);
      const url = window.URL.createObjectURL(blob);

      // 🟢 新增：取得當前時間並格式化為 YYYYMMDD_HHmm
      const now = new Date();
      const year = now.getFullYear();
      const month = String(now.getMonth() + 1).padStart(2, "0");
      const day = String(now.getDate()).padStart(2, "0");
      const hours = String(now.getHours()).padStart(2, "0");
      const minutes = String(now.getMinutes()).padStart(2, "0");
      const timestamp = `${year}${month}${day}_${hours}${minutes}`;

      const a = document.createElement("a");
      a.href = url;
      a.download = `model_ratios_${timestamp}.xlsx`;
      document.body.appendChild(a);
      a.click();
      a.remove();
      window.URL.revokeObjectURL(url);

      toast.success(t("Export successful!"));
    } catch (err: any) {
      toast.error(
        t("Export failed: ") + (err?.message || t("Insufficient permissions or please re-login"))
      );
    }
  }, [t]);

  // --- 終極修正版：匯入 Excel 邏輯 ---
  const handleImportExcel = useCallback(
    async (e: React.ChangeEvent<HTMLInputElement>) => {
      const file = e.target.files?.[0];
      if (!file) return;

      const formData = new FormData();
      formData.append("file", file, file.name);

      setIsUploading(true);
      try {
        // 1. 取得認證 Header
        const authHeaders = (await getFreshAuthHeaders()) as Record<string, string>;

        // 2. 🟢 建立乾淨的 Headers 物件，徹底排除任何大小寫形式的 Content-Type
        const headers: Record<string, string> = {};
        for (const [key, value] of Object.entries(authHeaders)) {
          if (key.toLowerCase() !== "content-type") {
            headers[key] = value;
          }
        }

        // 3. 使用原生 fetch 發送 FormData
        const res = await fetch("/api/option/import_model_ratios", {
          method: "POST",
          headers, // 這裡只保留 Authorization/Session 等憑證，完全不帶 Content-Type
          body: formData, // 讓瀏覽器自動生成帶 boundary 的 multipart/form-data
        });

        const data = await res.json();

        if (res.ok && data?.success) {
          toast.success(data?.message || t("Model prices successfully updated in bulk!"));
          window.location.reload();
        } else {
          toast.error(data?.message || t("Import failed"));
        }
      } catch (err: any) {
        toast.error(
          t("File upload failed: ") + (err?.message || t("Upload error"))
        );
      } finally {
        setIsUploading(false);
        if (e.target) e.target.value = "";
      }
    },
    [t]
  );

  return (
    <div className="space-y-6">
      {!isUnsetVariant && (
        <div className="flex flex-wrap justify-end gap-2">
          {/* 隱藏的 File Input */}
          <input
            type="file"
            name="file"
            ref={fileInputRef}
            accept=".xlsx, .xls"
            className="hidden"
            onChange={handleImportExcel}
          />

          {/* 匯入 Excel 按鈕 */}
          <Button
            type="button"
            variant="outline"
            size="sm"
            onClick={() => fileInputRef.current?.click()}
            disabled={isUploading}
          >
            <Upload className="mr-2 h-4 w-4" />
            {isUploading ? t("Importing...") : t("Import Excel")}
          </Button>

          {/* 匯出 Excel 按鈕 */}
          <Button
            type="button"
            variant="outline"
            size="sm"
            onClick={handleExportExcel}
          >
            <Download className="mr-2 h-4 w-4" />
            {t("Export Excel")}
          </Button>

          <Button
            type="button"
            variant="destructive"
            size="sm"
            onClick={onReset}
            disabled={isResetting}
          >
            <RotateCcw data-icon="inline-start" />
            {t("Reset prices")}
          </Button>

          {editMode === "json" && (
            <Button
              type="button"
              size="sm"
              onClick={handleSave}
              disabled={isSaving}
            >
              <Save data-icon="inline-start" />
              {isSaving ? t("Saving...") : t("Save model prices")}
            </Button>
          )}

          <Button variant="outline" size="sm" onClick={toggleEditMode}>
            {editMode === "visual" ? (
              <>
                <Code2 className="mr-2 h-4 w-4" />
                {t("Switch to JSON")}
              </>
            ) : (
              <>
                <Eye className="mr-2 h-4 w-4" />
                {t("Switch to Visual")}
              </>
            )}
          </Button>
        </div>
      )}

      <Form {...form}>
        {editMode === "visual" ? (
          <div className="space-y-6">
            <ModelRatioVisualEditor
              ref={visualEditorRef}
              savedModelPrice={savedValues.ModelPrice}
              savedModelRatio={savedValues.ModelRatio}
              savedCacheRatio={savedValues.CacheRatio}
              savedCreateCacheRatio={savedValues.CreateCacheRatio}
              savedCompletionRatio={savedValues.CompletionRatio}
              savedImageRatio={savedValues.ImageRatio}
              savedAudioRatio={savedValues.AudioRatio}
              savedAudioCompletionRatio={savedValues.AudioCompletionRatio}
              savedBillingMode={savedValues.BillingMode}
              savedBillingExpr={savedValues.BillingExpr}
              modelPrice={form.watch("ModelPrice")}
              modelRatio={form.watch("ModelRatio")}
              cacheRatio={form.watch("CacheRatio")}
              createCacheRatio={form.watch("CreateCacheRatio")}
              completionRatio={form.watch("CompletionRatio")}
              imageRatio={form.watch("ImageRatio")}
              audioRatio={form.watch("AudioRatio")}
              audioCompletionRatio={form.watch("AudioCompletionRatio")}
              billingMode={form.watch("BillingMode")}
              billingExpr={form.watch("BillingExpr")}
              candidateModelNames={
                isUnsetVariant ? enabledModelsQuery.data?.data : undefined
              }
              candidateModelsLoading={
                isUnsetVariant && enabledModelsQuery.isLoading
              }
              filterMode={isUnsetVariant ? "unset" : "all"}
              onSave={handleSave}
              isSaving={isSaving}
              onChange={(field, value) => {
                const fieldMap: Record<string, keyof ModelFormValues> = {
                  "billing_setting.billing_mode": "BillingMode",
                  "billing_setting.billing_expr": "BillingExpr",
                };
                const formField =
                  fieldMap[field] || (field as keyof ModelFormValues);
                handleFieldChange(formField, value);
              }}
            />

            {!isUnsetVariant && (
              <FormField
                control={form.control}
                name="ExposeRatioEnabled"
                render={({ field }) => (
                  <SettingsSwitchItem>
                    <SettingsSwitchContent>
                      <FormLabel>{t("Expose ratio API")}</FormLabel>
                      <FormDescription>
                        {t(
                          "Allow clients to query configured ratios via `/api/ratio`."
                        )}
                      </FormDescription>
                    </SettingsSwitchContent>
                    <FormControl>
                      <Switch
                        checked={field.value}
                        onCheckedChange={field.onChange}
                      />
                    </FormControl>
                  </SettingsSwitchItem>
                )}
              />
            )}
          </div>
        ) : (
          <SettingsForm onSubmit={form.handleSubmit(onSave)}>
            <div className="grid min-w-0 gap-x-5 gap-y-8 lg:grid-cols-2 2xl:grid-cols-3">
              {modelJsonFields.map((config) => (
                <ModelJsonTextareaField
                  key={config.name}
                  form={form}
                  name={config.name}
                  label={t(config.labelKey)}
                  description={t(config.descriptionKey)}
                />
              ))}
            </div>

            <FormField
              control={form.control}
              name="ExposeRatioEnabled"
              render={({ field }) => (
                <SettingsSwitchItem>
                  <SettingsSwitchContent>
                    <FormLabel>{t("Expose ratio API")}</FormLabel>
                    <FormDescription>
                      {t(
                        "Allow clients to query configured ratios via `/api/ratio`."
                      )}
                    </FormDescription>
                  </SettingsSwitchContent>
                  <FormControl>
                    <Switch
                      checked={field.value}
                      onCheckedChange={field.onChange}
                    />
                  </FormControl>
                </SettingsSwitchItem>
              )}
            />
          </SettingsForm>
        )}
      </Form>
    </div>
  );
});
