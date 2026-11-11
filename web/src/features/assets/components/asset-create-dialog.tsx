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
import { zodResolver } from '@hookform/resolvers/zod'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useId, useRef, useState } from 'react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Dialog } from '@/components/dialog'
import { ErrorState } from '@/components/error-state'
import { FilePicker } from '@/components/file-picker'
import { LoadingState } from '@/components/loading-state'
import { Button } from '@/components/ui/button'
import {
  Form,
  FormControl,
  FormDescription,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { Input } from '@/components/ui/input'
import { toIntlLocale } from '@/i18n/languages'
import { formatNumber } from '@/lib/format'
import { handleServerError } from '@/lib/handle-server-error'
import { getServerErrorMessage } from '@/lib/server-error-message'

import {
  assetQueryKeys,
  executeAssetAction,
  getAssetConfig,
  uploadAsset,
} from '../api'
import { deriveAssetName, inspectAssetFile } from '../lib/asset-source'
import { assetCreateSchema } from '../lib/schemas'
import type { AssetGroup, AssetScope, AssetSizeLimits } from '../types'

interface AssetFormValues {
  name: string
  source: 'url' | 'file'
  url: string
}

export function AssetCreateDialog({
  group,
  scope,
  onClose,
}: {
  group: AssetGroup
  scope: AssetScope
  onClose: () => void
}) {
  const { t, i18n } = useTranslation()
  const id = useId()
  const client = useQueryClient()
  const submitting = useRef(false)
  const [file, setFile] = useState<File | null>(null)
  const [selectionError, setSelectionError] = useState('')
  const form = useForm<AssetFormValues>({
    resolver: zodResolver(assetCreateSchema(t)),
    defaultValues: { name: '', source: 'url', url: '' },
  })
  const url = form.watch('url')
  const automaticName = deriveAssetName(
    file ? { fileName: file.name } : { url }
  )
  const config = useQuery({
    queryKey: assetQueryKeys.config,
    queryFn: ({ signal }) => getAssetConfig(signal),
    meta: { errorToast: false },
  })
  const limits = config.data?.size_limits_mb
  const limitsReady = Boolean(
    limits &&
    [limits.image, limits.video, limits.audio].every(
      (limit) => Number.isFinite(limit) && limit > 0
    )
  )
  const canSubmit = config.data?.enabled && limitsReady && !config.isError
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const sizeHint =
    limitsReady && limits
      ? t(
          'Images < {{image}} MB · Video ≤ {{video}} MB · Audio ≤ {{audio}} MB',
          {
            image: formatNumber(limits.image, locale),
            video: formatNumber(limits.video, locale),
            audio: formatNumber(limits.audio, locale),
          }
        )
      : ''

  function fileAdmissionError(candidate: File): string {
    if (candidate.size === 0) return t('The file is empty.')
    const inspection = inspectAssetFile(candidate)
    if (inspection.unsupported) {
      return t('Only image, video, and audio files are supported.')
    }
    if (!limitsReady || !limits) return ''
    const type = inspection.type
    const limit = type
      ? limits[type.toLowerCase() as keyof AssetSizeLimits]
      : Math.max(limits.image, limits.video, limits.audio)
    let maxBytes = Math.max(
      limits.image * 1000000 - 1,
      limits.video * 1000000,
      limits.audio * 1000000
    )
    if (type) maxBytes = limit * 1000000
    if (type === 'Image') maxBytes -= 1
    if (candidate.size <= maxBytes) return ''
    return type === 'Image'
      ? t('File size must be less than {{limit}} MB.', {
          limit: formatNumber(limit, locale),
        })
      : t('File size must not exceed {{limit}} MB.', {
          limit: formatNumber(limit, locale),
        })
  }

  const currentFileError = file ? fileAdmissionError(file) : ''
  const mutation = useMutation({
    mutationFn: ({
      values,
      selectedFile,
    }: {
      values: AssetFormValues
      selectedFile: File | null
    }) => {
      const fields = {
        GroupId: group.Id,
        ProjectName: group.ProjectName,
        Name:
          values.name ||
          deriveAssetName(
            selectedFile ? { fileName: selectedFile.name } : { url: values.url }
          ),
      }
      if (!selectedFile) {
        return executeAssetAction<{ Id: string }>(
          'CreateAsset',
          { ...fields, URL: values.url },
          scope
        )
      }
      const body = new FormData()
      Object.entries(fields).forEach(([key, value]) => body.append(key, value))
      body.append('file', selectedFile)
      return uploadAsset(body, scope)
    },
    onSuccess: (_, { selectedFile }) => {
      void client.invalidateQueries({ queryKey: assetQueryKeys.all })
      toast.success(
        selectedFile ? t('Asset added') : t('Asset submitted for processing')
      )
      onClose()
    },
    onError: (error) => handleServerError(error),
    onSettled: () => {
      submitting.current = false
    },
  })
  return (
    <Dialog
      open
      onOpenChange={(open) => !open && !submitting.current && onClose()}
      title={t('Add asset')}
      description={group.Name}
      contentHeight='auto'
      footer={
        <>
          <Button
            variant='outline'
            onClick={() => !submitting.current && onClose()}
            disabled={mutation.isPending}
          >
            {t('Cancel')}
          </Button>
          <Button
            type='submit'
            form={id}
            disabled={
              mutation.isPending ||
              !canSubmit ||
              Boolean(currentFileError) ||
              (!file && !url.trim())
            }
          >
            {mutation.isPending ? t('Uploading...') : t('Add asset')}
          </Button>
        </>
      }
    >
      <Form {...form}>
        <form
          id={id}
          className='space-y-4'
          onSubmit={form.handleSubmit((values) => {
            if (submitting.current || !canSubmit || currentFileError) return
            submitting.current = true
            mutation.mutate({ values, selectedFile: file })
          })}
        >
          <FormField
            control={form.control}
            name='url'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Asset URL')}</FormLabel>
                <FormControl>
                  <Input
                    {...field}
                    autoFocus
                    type='url'
                    placeholder='https://'
                    disabled={mutation.isPending || Boolean(file)}
                  />
                </FormControl>
                <FormMessage />
              </FormItem>
            )}
          />
          <FilePicker
            fileName={file?.name ?? ''}
            label={t('File')}
            prompt={t('Select or drop a file here')}
            accept='image/*,video/*,audio/*,.ogg,.mp4,.webm'
            disabled={mutation.isPending}
            description={
              file
                ? t('{{size}} bytes', { size: formatNumber(file.size, locale) })
                : t('Images, videos, and audio only')
            }
            error={selectionError || currentFileError}
            onSelect={(files) => {
              if (submitting.current) return
              if (files.length !== 1) {
                setSelectionError(t('Select one file at a time.'))
                return
              }
              const candidate = files[0]
              const error = fileAdmissionError(candidate)
              if (error) {
                setSelectionError(error)
                return
              }
              setSelectionError('')
              setFile(candidate)
              form.setValue('source', 'file')
              form.clearErrors('url')
            }}
            onRemove={() => {
              if (submitting.current) return
              setFile(null)
              setSelectionError('')
              form.setValue('source', 'url')
            }}
          />
          {config.isPending && (
            <LoadingState inline size='sm' message={t('Loading...')} />
          )}
          {config.isError && (
            <ErrorState
              className='min-h-0 p-3'
              description={getServerErrorMessage(config.error)}
              onRetry={() => void config.refetch()}
            />
          )}
          {sizeHint && (
            <p className='text-muted-foreground text-xs'>{sizeHint}</p>
          )}
          <FormField
            control={form.control}
            name='name'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Name')}</FormLabel>
                <FormControl>
                  <Input
                    {...field}
                    placeholder={automaticName}
                    disabled={mutation.isPending}
                  />
                </FormControl>
                <FormDescription>
                  {t('Leave blank to use the URL or file name.')}
                </FormDescription>
                <FormMessage />
              </FormItem>
            )}
          />
        </form>
      </Form>
    </Dialog>
  )
}
