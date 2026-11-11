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
import { type ComponentProps, useId, useState } from 'react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import type { z } from 'zod'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { Dialog } from '@/components/dialog'
import { ErrorState } from '@/components/error-state'
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
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Switch } from '@/components/ui/switch'
import { handleServerError } from '@/lib/handle-server-error'
import { getServerErrorMessage } from '@/lib/server-error-message'

import {
  assetQueryKeys,
  getAssetStorage,
  saveAssetStorage,
  testAssetStorage,
} from '../api'
import { assetStorageSchema } from '../lib/schemas'
import {
  assetStoragePathStyle,
  assetStorageProviders,
  normalizeAssetStorageEndpoint,
} from '../lib/storage-providers'
import type { AssetStorageConfig, AssetStorageUpdate } from '../types'
import { AssetSecretInput } from './asset-secret-input'

type AssetStorageSettingsDialogProps = {
  onClose: () => void
  finalFocus?: ComponentProps<typeof Dialog>['finalFocus']
}

export function AssetStorageSettingsDialog(
  props: AssetStorageSettingsDialogProps
) {
  const { t } = useTranslation()
  const query = useQuery({
    queryKey: assetQueryKeys.storage,
    queryFn: ({ signal }) => getAssetStorage(signal),
    refetchOnMount: 'always',
    refetchOnWindowFocus: false,
    refetchOnReconnect: false,
    meta: { errorToast: false },
  })
  if (query.isSuccess && query.isFetchedAfterMount) {
    return <AssetStorageForm config={query.data} {...props} />
  }
  return (
    <Dialog
      open
      onOpenChange={(open) => !open && props.onClose()}
      title={t('Storage settings')}
      finalFocus={props.finalFocus}
      footer={
        <Button variant='outline' onClick={props.onClose}>
          {t('Close')}
        </Button>
      }
    >
      {query.isError ? (
        <ErrorState
          description={getServerErrorMessage(query.error)}
          onRetry={() => void query.refetch()}
        />
      ) : (
        <LoadingState />
      )}
    </Dialog>
  )
}

function AssetStorageForm(
  props: AssetStorageSettingsDialogProps & { config: AssetStorageConfig }
) {
  const { t } = useTranslation()
  const id = useId()
  const [confirmClose, setConfirmClose] = useState(false)
  const client = useQueryClient()
  const schema = assetStorageSchema(t, props.config.has_secret)
  const { has_secret: _hasSecret, ...defaults } = props.config
  const form = useForm<z.infer<typeof schema>>({
    resolver: zodResolver(schema),
    defaultValues: {
      ...defaults,
      provider:
        assetStorageProviders.find((value) => value === defaults.provider) ??
        'other',
      secret_key: '',
    },
  })
  const mutation = useMutation({
    gcTime: 0,
    mutationFn: async ({
      values,
      test,
    }: {
      values: AssetStorageUpdate
      test: boolean
    }) => (test ? testAssetStorage(values) : saveAssetStorage(values)),
    onSuccess: (saved, { test }) => {
      if (test) {
        toast.success(t('Storage connection verified'))
        return
      }
      if (saved) {
        client.setQueryData(assetQueryKeys.storage, saved)
        const { has_secret: _savedSecret, ...fields } = saved
        form.reset({
          ...fields,
          provider:
            assetStorageProviders.find((value) => value === fields.provider) ??
            'other',
          secret_key: '',
        })
      }
      void client.invalidateQueries({ queryKey: assetQueryKeys.all })
      toast.success(t('Saved successfully'))
    },
    onError: (error) => handleServerError(error),
    onSettled: (): void => mutation.reset(),
  })
  const busy = mutation.isPending || form.formState.isSubmitting
  const requestClose = () => {
    if (busy) return
    if (form.formState.isDirty) setConfirmClose(true)
    else props.onClose()
  }
  const providers = [
    { value: 'other', label: t('Other S3-compatible storage') },
    { value: 'aws', label: t('Amazon S3') },
    { value: 'tos', label: t('Volcengine TOS') },
    { value: 'oss', label: t('Alibaba Cloud OSS') },
    { value: 'cos', label: t('Tencent Cloud COS') },
    { value: 'minio', label: t('MinIO') },
    { value: 'ceph', label: t('Ceph RGW') },
    { value: 'oci', label: t('Oracle Cloud OCI') },
  ]
  const submit = (test: boolean) =>
    form.handleSubmit((values) => {
      if (test) {
        const validation = schema.safeParse({ ...values, enabled: true })
        if (!validation.success) {
          for (const issue of validation.error.issues) {
            form.setError(issue.path[0] as keyof typeof values, {
              message: issue.message,
            })
          }
          return
        }
      }
      form.setValue('endpoint', values.endpoint, { shouldDirty: true })
      const { secret_key, ...fields } = values
      mutation.mutate({
        values: { ...fields, ...(secret_key ? { secret_key } : {}) },
        test,
      })
    })
  return (
    <>
      <Dialog
        open
        onOpenChange={(open) => !open && requestClose()}
        title={t('Storage settings')}
        finalFocus={props.finalFocus}
        description={t(
          'Store assets in a private S3-compatible bucket shared by all nodes.'
        )}
        footer={
          <>
            <Button variant='outline' onClick={requestClose} disabled={busy}>
              {t('Close')}
            </Button>
            <Button
              form={id}
              type='submit'
              disabled={busy || !form.formState.isDirty}
            >
              {mutation.isPending && !mutation.variables.test
                ? t('Saving...')
                : t('Save')}
            </Button>
          </>
        }
      >
        <Form {...form}>
          <form
            id={id}
            className='grid min-w-0 gap-x-5 gap-y-6 sm:grid-cols-2'
            onSubmit={submit(false)}
          >
            <FormField
              control={form.control}
              name='enabled'
              render={({ field }) => (
                <FormItem className='flex min-w-0 items-center justify-between gap-4 py-2.5 sm:col-span-2'>
                  <div className='min-w-0 space-y-0.5'>
                    <FormLabel>{t('Enable asset library')}</FormLabel>
                  </div>
                  <FormControl>
                    <Switch
                      checked={field.value}
                      onCheckedChange={field.onChange}
                      disabled={busy}
                    />
                  </FormControl>
                  <FormMessage />
                </FormItem>
              )}
            />
            <FormField
              control={form.control}
              name='provider'
              render={({ field }) => (
                <FormItem className='min-w-0 sm:col-span-2'>
                  <FormLabel>{t('S3 provider')}</FormLabel>
                  <Select
                    items={providers}
                    value={field.value}
                    onValueChange={(value) => {
                      const provider = assetStorageProviders.find(
                        (item) => item === value
                      )
                      if (!provider) return
                      field.onChange(provider)
                      form.setValue(
                        'force_path_style',
                        assetStoragePathStyle(
                          provider,
                          form.getValues('endpoint'),
                          form.getValues('force_path_style')
                        ),
                        { shouldDirty: true }
                      )
                    }}
                    disabled={busy}
                  >
                    <FormControl>
                      <SelectTrigger className='w-full'>
                        <SelectValue />
                      </SelectTrigger>
                    </FormControl>
                    <SelectContent>
                      {providers.map((provider) => (
                        <SelectItem key={provider.value} value={provider.value}>
                          {provider.label}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                  <FormMessage />
                </FormItem>
              )}
            />
            <div className='grid min-w-0 gap-x-5 gap-y-6 sm:col-span-2 sm:grid-cols-4'>
              <FormField
                control={form.control}
                name='endpoint'
                render={({ field }) => (
                  <FormItem className='min-w-0 sm:col-span-2'>
                    <FormLabel>{t('S3 endpoint')}</FormLabel>
                    <FormControl>
                      <Input
                        {...field}
                        inputMode='url'
                        disabled={busy}
                        autoComplete='off'
                        onChange={(event) => {
                          const endpoint = event.target.value
                          if (form.getValues('provider') === 'oci') {
                            const current = form.getValues('force_path_style')
                            const previous = assetStoragePathStyle(
                              'oci',
                              field.value,
                              current
                            )
                            const next = assetStoragePathStyle(
                              'oci',
                              endpoint,
                              current
                            )
                            if (previous !== next) {
                              form.setValue('force_path_style', next, {
                                shouldDirty: true,
                              })
                            }
                          }
                          field.onChange(endpoint)
                        }}
                        onBlur={() => {
                          field.onChange(
                            normalizeAssetStorageEndpoint(field.value)
                          )
                          field.onBlur()
                        }}
                      />
                    </FormControl>
                    <FormDescription>
                      {t(
                        'HTTPS is added if omitted. Use http:// for storage without TLS.'
                      )}
                    </FormDescription>
                    <FormMessage />
                  </FormItem>
                )}
              />
              {(['region', 'bucket'] as const).map((name) => (
                <FormField
                  key={name}
                  control={form.control}
                  name={name}
                  render={({ field }) => (
                    <FormItem className='min-w-0 content-start'>
                      <FormLabel>
                        {name === 'region' ? t('Region') : t('Bucket')}
                      </FormLabel>
                      <FormControl>
                        <Input {...field} disabled={busy} autoComplete='off' />
                      </FormControl>
                      <FormMessage />
                    </FormItem>
                  )}
                />
              ))}
            </div>
            <FormField
              control={form.control}
              name='access_key'
              render={({ field }) => (
                <FormItem className='min-w-0 content-start'>
                  <FormLabel>{t('Access key')}</FormLabel>
                  <FormControl>
                    <Input {...field} disabled={busy} autoComplete='off' />
                  </FormControl>
                  <FormMessage />
                </FormItem>
              )}
            />
            <FormField
              control={form.control}
              name='secret_key'
              render={({ field }) => (
                <FormItem className='min-w-0'>
                  <FormLabel>{t('Secret key')}</FormLabel>
                  <FormControl>
                    <AssetSecretInput
                      {...field}
                      config={props.config}
                      disabled={busy}
                      autoComplete='new-password'
                    />
                  </FormControl>
                  <FormDescription>
                    {props.config.has_secret
                      ? t('A secret is saved. Leave blank to keep it.')
                      : t('No secret is saved.')}
                  </FormDescription>
                  <FormMessage />
                </FormItem>
              )}
            />
            <FormField
              control={form.control}
              name='force_path_style'
              render={({ field }) => (
                <FormItem className='flex min-w-0 items-center justify-between gap-4 py-2.5 sm:col-span-2'>
                  <div className='min-w-0 space-y-0.5'>
                    <FormLabel>{t('Use path-style addressing')}</FormLabel>
                    <FormDescription>
                      {t(
                        'Set automatically for the selected provider. Adjust if your deployment requires it.'
                      )}
                    </FormDescription>
                  </div>
                  <FormControl>
                    <Switch
                      checked={field.value}
                      onCheckedChange={field.onChange}
                      disabled={busy}
                    />
                  </FormControl>
                </FormItem>
              )}
            />
            <div className='flex flex-wrap items-center gap-3 sm:col-span-2'>
              <Button
                type='button'
                variant='outline'
                onClick={submit(true)}
                disabled={busy}
              >
                {t('Test Connection')}
              </Button>
              <p className='text-muted-foreground text-xs'>
                {t(
                  'Tests upload, signed read, and deletion of a temporary object.'
                )}
              </p>
            </div>
          </form>
        </Form>
      </Dialog>
      <ConfirmDialog
        open={confirmClose}
        onOpenChange={setConfirmClose}
        title={t('Unsaved changes')}
        desc={t('You have unsaved changes. Are you sure you want to leave?')}
        confirmText={t('Discard changes')}
        handleConfirm={props.onClose}
      />
    </>
  )
}
