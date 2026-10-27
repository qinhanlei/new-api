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
import { useId, useState } from 'react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Dialog } from '@/components/dialog'
import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { Button } from '@/components/ui/button'
import {
  Form,
  FormControl,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { Input } from '@/components/ui/input'
import { NativeSelect } from '@/components/ui/native-select'
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
import { assetCreateSchema } from '../lib/schemas'
import type {
  AssetGroup,
  AssetScope,
  AssetSizeLimits,
  AssetType,
} from '../types'

interface AssetFormValues {
  name: string
  type: AssetType
  source: 'url' | 'file'
  url: string
}
const assetAccept: Record<AssetType, string> = {
  Image: 'image/*',
  Video: 'video/*',
  Audio: 'audio/*',
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
  const [file, setFile] = useState<File | null>(null)
  const [fileError, setFileError] = useState('')
  const form = useForm<AssetFormValues>({
    resolver: zodResolver(assetCreateSchema(t)),
    defaultValues: { name: '', type: 'Image', source: 'url', url: '' },
  })
  const source = form.watch('source')
  const type = form.watch('type')
  const config = useQuery({
    queryKey: assetQueryKeys.config,
    queryFn: ({ signal }) => getAssetConfig(signal),
    meta: { errorToast: false },
  })
  const limit =
    config.data?.size_limits_mb?.[type.toLowerCase() as keyof AssetSizeLimits]
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const sizeHint =
    type === 'Image'
      ? t('File size must be less than {{limit}} MB.', {
          limit: formatNumber(limit, locale),
        })
      : t('File size must not exceed {{limit}} MB.', {
          limit: formatNumber(limit, locale),
        })
  let currentFileError = fileError
  if (file && file.size === 0) {
    currentFileError = t('The file is empty.')
  } else if (file && limit !== undefined) {
    const maxBytes = limit * 1000000 - (type === 'Image' ? 1 : 0)
    if (file.size > maxBytes) currentFileError = sizeHint
  }
  const mutation = useMutation({
    mutationFn: (values: AssetFormValues) => {
      const fields = {
        GroupId: group.Id,
        ProjectName: group.ProjectName,
        AssetType: values.type,
        Name: values.name,
      }
      if (values.source === 'url') {
        return executeAssetAction<{ Id: string }>(
          'CreateAsset',
          { ...fields, URL: values.url },
          scope
        )
      }
      if (!file) throw new Error(t('Select a file'))
      const body = new FormData()
      Object.entries(fields).forEach(([key, value]) => body.append(key, value))
      body.append('file', file)
      return uploadAsset(body, scope)
    },
    onSuccess: () => {
      void client.invalidateQueries({ queryKey: assetQueryKeys.all })
      toast.success(t('Asset added'))
      onClose()
    },
    onError: (error) => handleServerError(error),
  })
  return (
    <Dialog
      open
      onOpenChange={(open) => !open && !mutation.isPending && onClose()}
      title={t('Add asset')}
      description={group.Name}
      contentHeight='auto'
      footer={
        <>
          <Button
            variant='outline'
            onClick={onClose}
            disabled={mutation.isPending}
          >
            {t('Cancel')}
          </Button>
          <Button
            type='submit'
            form={id}
            disabled={
              mutation.isPending ||
              !config.data?.enabled ||
              limit === undefined ||
              config.isError
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
            if (
              !config.data?.enabled ||
              limit === undefined ||
              config.isError
            ) {
              return
            }
            if (values.source === 'file' && !file) {
              setFileError(t('Select a file'))
              return
            }
            if (values.source === 'file' && currentFileError) return
            mutation.mutate(values)
          })}
        >
          <FormField
            control={form.control}
            name='name'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Name')}</FormLabel>
                <FormControl>
                  <Input {...field} autoFocus maxLength={64} />
                </FormControl>
                <FormMessage />
              </FormItem>
            )}
          />
          <FormField
            control={form.control}
            name='type'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Asset type')}</FormLabel>
                <FormControl>
                  <NativeSelect
                    {...field}
                    className='w-full'
                    onChange={(event) => {
                      field.onChange(event)
                      setFile(null)
                      setFileError('')
                    }}
                  >
                    <option value='Image'>{t('Image')}</option>
                    <option value='Video'>{t('Video')}</option>
                    <option value='Audio'>{t('Audio')}</option>
                  </NativeSelect>
                </FormControl>
                <FormMessage />
              </FormItem>
            )}
          />
          <FormField
            control={form.control}
            name='source'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Source')}</FormLabel>
                <FormControl>
                  <NativeSelect
                    {...field}
                    className='w-full'
                    onChange={(event) => {
                      field.onChange(event)
                      setFile(null)
                      setFileError('')
                    }}
                  >
                    <option value='url'>{t('Import URL')}</option>
                    <option value='file'>{t('Upload file')}</option>
                  </NativeSelect>
                </FormControl>
              </FormItem>
            )}
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
          {limit !== undefined && (
            <p
              id={`${id}-size-limit`}
              className='text-muted-foreground text-sm'
            >
              {sizeHint}{' '}
              {t(
                '1 MB = 1,000,000 bytes. Other requirements are checked by the upstream.'
              )}
            </p>
          )}
          {source === 'url' ? (
            <FormField
              control={form.control}
              name='url'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t('Asset URL')}</FormLabel>
                  <FormControl>
                    <Input {...field} type='url' placeholder='https://' />
                  </FormControl>
                  <FormMessage />
                </FormItem>
              )}
            />
          ) : (
            <div className='space-y-2'>
              <label htmlFor={`${id}-file`} className='text-sm font-medium'>
                {t('File')}
              </label>
              <Input
                key={type}
                id={`${id}-file`}
                type='file'
                accept={assetAccept[type]}
                aria-invalid={Boolean(currentFileError)}
                aria-describedby={
                  currentFileError
                    ? `${id}-size-limit ${id}-file-error`
                    : `${id}-size-limit`
                }
                onChange={(event) => {
                  setFile(event.target.files?.[0] ?? null)
                  setFileError('')
                }}
              />
              {currentFileError && (
                <p
                  id={`${id}-file-error`}
                  role='alert'
                  className='text-destructive text-sm'
                >
                  {currentFileError}
                </p>
              )}
            </div>
          )}
        </form>
      </Form>
    </Dialog>
  )
}
