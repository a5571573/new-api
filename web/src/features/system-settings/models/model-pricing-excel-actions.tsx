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
import { useQueryClient } from '@tanstack/react-query'
import { Download, Upload } from 'lucide-react'
import { useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { Button } from '@/components/ui/button'
import { invalidateModelPricing } from '@/features/model-pricing/api'
import { api } from '@/lib/api'
import { handleServerError } from '@/lib/handle-server-error'
import { createServerError } from '@/lib/server-error-message'

type PricingFieldChange = {
  field: string
  before: string
  after: string
}

type PricingModelChange = {
  model_name: string
  fields: PricingFieldChange[]
}

type PricingImportResponse = {
  success: boolean
  message?: string
  data?: {
    applied?: boolean
    changes?: PricingModelChange[]
    unchanged?: number
    errors?: string[]
  }
}

type ImportReview =
  | { kind: 'changes'; file: File; changes: PricingModelChange[] }
  | { kind: 'errors'; message: string; errors: string[] }

async function uploadPricingWorkbook(
  file: File,
  dryRun: boolean
): Promise<PricingImportResponse> {
  const body = new FormData()
  body.append('file', file)
  const res = await api.post('/api/option/import_model_ratios', body, {
    params: dryRun ? { dry_run: 'true' } : undefined,
  })
  return res.data
}

type ModelPricingExcelActionsProps = {
  // Reloads the settings page's pricing baseline after an import is saved.
  onImported: () => Promise<void>
}

export function ModelPricingExcelActions(props: ModelPricingExcelActionsProps) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const fileInputRef = useRef<HTMLInputElement>(null)
  const [isExporting, setIsExporting] = useState(false)
  const [isImporting, setIsImporting] = useState(false)
  const [review, setReview] = useState<ImportReview | null>(null)

  const handleExport = async () => {
    setIsExporting(true)
    try {
      const response = await api.get<Blob>('/api/option/export_model_ratios', {
        responseType: 'blob',
      })
      const url = URL.createObjectURL(response.data)
      const link = document.createElement('a')
      link.href = url
      link.download = `model_pricing_${Date.now()}.xlsx`
      document.body.append(link)
      link.click()
      link.remove()
      URL.revokeObjectURL(url)
    } catch (error) {
      handleServerError(error, t('Failed to export model pricing'))
    } finally {
      setIsExporting(false)
    }
  }

  // Failed imports list every bad row so the whole sheet can be fixed at once.
  const showImportFailure = (response: PricingImportResponse) => {
    const errors = response.data?.errors ?? []
    if (errors.length === 0) {
      handleServerError(createServerError(response, t('Import failed')))
      return
    }
    setReview({
      kind: 'errors',
      message: response.message ?? t('Import failed'),
      errors,
    })
  }

  const handleFileSelected = async (
    event: React.ChangeEvent<HTMLInputElement>
  ) => {
    const file = event.target.files?.[0]
    event.target.value = ''
    if (!file) return

    setIsImporting(true)
    try {
      const response = await uploadPricingWorkbook(file, true)
      if (!response.success) {
        showImportFailure(response)
        return
      }
      const changes = response.data?.changes ?? []
      if (changes.length === 0) {
        toast.info(t('No pricing changes found in the file'))
        return
      }
      setReview({ kind: 'changes', file, changes })
    } catch (error) {
      handleServerError(error, t('Import failed'))
    } finally {
      setIsImporting(false)
    }
  }

  const handleConfirmImport = async () => {
    if (review?.kind !== 'changes') return
    setIsImporting(true)
    try {
      const response = await uploadPricingWorkbook(review.file, false)
      if (!response.success) {
        showImportFailure(response)
        return
      }
      const count = response.data?.changes?.length ?? 0
      toast.success(t('Updated pricing for {{count}} models', { count }))
      setReview(null)
      await invalidateModelPricing(queryClient)
      await props.onImported()
    } catch (error) {
      handleServerError(error, t('Import failed'))
    } finally {
      setIsImporting(false)
    }
  }

  const handleChooseAnotherFile = () => {
    setReview(null)
    fileInputRef.current?.click()
  }

  const displayValue = (value: string) => value || t('(default)')

  return (
    <>
      <input
        ref={fileInputRef}
        type='file'
        accept='.xlsx'
        className='hidden'
        aria-hidden='true'
        tabIndex={-1}
        onChange={handleFileSelected}
      />
      <Button
        type='button'
        variant='outline'
        size='sm'
        onClick={() => fileInputRef.current?.click()}
        disabled={isImporting}
      >
        <Upload data-icon='inline-start' />
        {isImporting ? t('Importing...') : t('Import Excel')}
      </Button>
      <Button
        type='button'
        variant='outline'
        size='sm'
        onClick={handleExport}
        disabled={isExporting}
      >
        <Download data-icon='inline-start' />
        {isExporting ? t('Exporting...') : t('Export Excel')}
      </Button>

      {review?.kind === 'changes' && (
        <ConfirmDialog
          open
          onOpenChange={(open) => {
            if (!open && !isImporting) setReview(null)
          }}
          title={t('Confirm pricing import')}
          desc={t(
            '{{count}} models will be updated. Blank values use the system default.',
            { count: review.changes.length }
          )}
          confirmText={t('Import')}
          isLoading={isImporting}
          handleConfirm={handleConfirmImport}
        >
          <ul className='max-h-80 space-y-3 overflow-y-auto text-sm'>
            {review.changes.map((change) => (
              <li key={change.model_name}>
                <div className='font-medium break-all'>{change.model_name}</div>
                <ul className='text-muted-foreground mt-1 space-y-0.5'>
                  {change.fields.map((field) => (
                    <li key={field.field} className='break-all'>
                      {field.field}: {displayValue(field.before)} →{' '}
                      <span className='text-foreground'>
                        {displayValue(field.after)}
                      </span>
                    </li>
                  ))}
                </ul>
              </li>
            ))}
          </ul>
        </ConfirmDialog>
      )}

      {review?.kind === 'errors' && (
        <ConfirmDialog
          open
          onOpenChange={(open) => {
            if (!open) setReview(null)
          }}
          title={t('Import failed')}
          desc={`${review.message}. ${t('Fix these rows and import again.')}`}
          confirmText={t('Choose another file')}
          handleConfirm={handleChooseAnotherFile}
        >
          <ul className='text-destructive max-h-80 list-disc space-y-1 overflow-y-auto ps-5 text-sm break-all'>
            {review.errors.map((error) => (
              <li key={error}>{error}</li>
            ))}
          </ul>
        </ConfirmDialog>
      )}
    </>
  )
}
