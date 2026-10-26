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
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import type { z } from 'zod'

import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { PasswordInput } from '@/components/password-input'
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
import { Switch } from '@/components/ui/switch'
import {
  assetQueryKeys,
  getAssetStorage,
  saveAssetStorage,
  testAssetStorage,
} from '@/features/assets/api'
import { assetStorageSchema } from '@/features/assets/lib/schemas'
import type {
  AssetStorageConfig,
  AssetStorageUpdate,
} from '@/features/assets/types'
import {
  SecureVerificationDialog,
  useSecureVerification,
} from '@/features/auth/secure-verification'
import { handleServerError } from '@/lib/handle-server-error'
import { AuthOperationError } from '@/lib/secure-verification'
import { getServerErrorMessage } from '@/lib/server-error-message'

import {
  SettingsForm,
  SettingsSwitchContent,
  SettingsSwitchItem,
} from '../components/settings-form-layout'
import { SettingsPageFormActions } from '../components/settings-page-context'
import { SettingsSection } from '../components/settings-section'

export function AssetStorageSection() {
  const query = useQuery({
    queryKey: assetQueryKeys.storage,
    queryFn: ({ signal }) => getAssetStorage(signal),
    refetchOnWindowFocus: false,
    meta: { errorToast: false },
  })
  if (query.isPending) return <LoadingState />
  if (query.isError) {
    return (
      <ErrorState
        description={getServerErrorMessage(query.error)}
        onRetry={() => void query.refetch()}
      />
    )
  }
  return <AssetStorageForm config={query.data} />
}

function AssetStorageForm({ config }: { config: AssetStorageConfig }) {
  const { t } = useTranslation()
  const client = useQueryClient()
  const verification = useSecureVerification()
  const schema = assetStorageSchema(t, config.has_secret)
  const { has_secret: _hasSecret, ...defaults } = config
  const form = useForm<z.infer<typeof schema>>({
    resolver: zodResolver(schema),
    defaultValues: { ...defaults, secret_key: '' },
  })
  const mutation = useMutation({
    gcTime: 0,
    mutationFn: async ({
      values,
      proof,
      test,
    }: {
      values: AssetStorageUpdate
      proof: string
      test: boolean
    }) =>
      test ? testAssetStorage(values, proof) : saveAssetStorage(values, proof),
    onSuccess: (saved, { test }) => {
      if (test) {
        toast.success(t('Storage connection verified'))
        return
      }
      if (saved) {
        client.setQueryData(assetQueryKeys.storage, saved)
        const { has_secret: _savedSecret, ...fields } = saved
        form.reset({ ...fields, secret_key: '' })
      }
      void client.invalidateQueries({ queryKey: assetQueryKeys.all })
      toast.success(t('Saved successfully'))
    },
    onError: (error) => handleServerError(error),
    onSettled: (): void => mutation.reset(),
  })
  const busy = mutation.isPending || verification.isActive
  const submit = (test: boolean) =>
    form.handleSubmit(async (values) => {
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
      try {
        const proof = await verification.requestVerification({
          scope: test ? 'asset.storage.test' : 'asset.storage.write',
        })
        if (!proof) return
        const { secret_key, ...fields } = values
        mutation.mutate({
          values: { ...fields, ...(secret_key ? { secret_key } : {}) },
          proof: proof.proof_token,
          test,
        })
      } catch (error) {
        handleServerError(AuthOperationError.from(error))
      }
    })
  return (
    <SettingsSection title={t('Asset storage')}>
      <p className='text-muted-foreground text-sm'>
        {t(
          'Store assets in a private S3-compatible bucket shared by all nodes.'
        )}
      </p>
      <Form {...form}>
        <SettingsForm onSubmit={submit(false)}>
          <SettingsPageFormActions
            onSave={submit(false)}
            isSaving={busy}
            isSaveDisabled={!form.formState.isDirty}
          />
          <FormField
            control={form.control}
            name='enabled'
            render={({ field }) => (
              <SettingsSwitchItem>
                <SettingsSwitchContent>
                  <FormLabel>{t('Enable asset library')}</FormLabel>
                </SettingsSwitchContent>
                <FormControl>
                  <Switch
                    checked={field.value}
                    onCheckedChange={field.onChange}
                    disabled={busy}
                  />
                </FormControl>
                <FormMessage />
              </SettingsSwitchItem>
            )}
          />
          {(['endpoint', 'region', 'bucket', 'access_key'] as const).map(
            (name) => (
              <FormField
                key={name}
                control={form.control}
                name={name}
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>
                      {t(
                        {
                          endpoint: 'S3 endpoint',
                          region: 'Region',
                          bucket: 'Bucket',
                          access_key: 'Access key',
                        }[name]
                      )}
                    </FormLabel>
                    <FormControl>
                      <Input
                        {...field}
                        disabled={busy}
                        type={name === 'endpoint' ? 'url' : 'text'}
                        autoComplete='off'
                      />
                    </FormControl>
                    <FormMessage />
                  </FormItem>
                )}
              />
            )
          )}
          <FormField
            control={form.control}
            name='secret_key'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Secret key')}</FormLabel>
                <FormControl>
                  <PasswordInput
                    {...field}
                    disabled={busy}
                    autoComplete='new-password'
                  />
                </FormControl>
                <FormDescription>
                  {config.has_secret
                    ? t('A secret is saved. Leave blank to keep it.')
                    : t('No secret is saved.')}
                </FormDescription>
                <FormMessage />
              </FormItem>
            )}
          />
          <details className='space-y-3'>
            <summary className='cursor-pointer text-sm font-medium'>
              {t('Advanced settings')}
            </summary>
            <FormField
              control={form.control}
              name='force_path_style'
              render={({ field }) => (
                <SettingsSwitchItem>
                  <SettingsSwitchContent>
                    <FormLabel>{t('Use path-style addressing')}</FormLabel>
                    <FormDescription>
                      {t(
                        'Enable only if your S3-compatible service requires it.'
                      )}
                    </FormDescription>
                  </SettingsSwitchContent>
                  <FormControl>
                    <Switch
                      checked={field.value}
                      onCheckedChange={field.onChange}
                      disabled={busy}
                    />
                  </FormControl>
                </SettingsSwitchItem>
              )}
            />
          </details>
          <div className='flex flex-wrap items-center gap-3'>
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
        </SettingsForm>
      </Form>
      <SecureVerificationDialog {...verification.dialogProps} />
    </SettingsSection>
  )
}
