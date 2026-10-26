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
import { useId } from 'react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import type { z } from 'zod'

import { Dialog } from '@/components/dialog'
import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { PasswordInput } from '@/components/password-input'
import { Alert, AlertDescription } from '@/components/ui/alert'
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
import { NativeSelect } from '@/components/ui/native-select'
import { Switch } from '@/components/ui/switch'
import {
  assetQueryKeys,
  getAssetChannel,
  saveAssetChannel,
} from '@/features/assets/api'
import { assetChannelSchema } from '@/features/assets/lib/schemas'
import type {
  AssetChannelConfig,
  AssetChannelUpdate,
} from '@/features/assets/types'
import {
  SecureVerificationDialog,
  useSecureVerification,
} from '@/features/auth/secure-verification'
import {
  SettingsSwitchContent,
  SettingsSwitchItem,
} from '@/features/system-settings/components/settings-form-layout'
import { handleServerError } from '@/lib/handle-server-error'
import { AuthOperationError } from '@/lib/secure-verification'
import { getServerErrorMessage } from '@/lib/server-error-message'

import { isMultiKeyChannel } from '../../lib'
import type { Channel } from '../../types'

export function ChannelAssetsDialog({
  channel,
  onClose,
}: {
  channel: Channel
  onClose: () => void
}) {
  const { t } = useTranslation()
  const query = useQuery({
    queryKey: assetQueryKeys.channel(channel.id),
    queryFn: ({ signal }) => getAssetChannel(channel.id, signal),
    refetchOnWindowFocus: false,
    meta: { errorToast: false },
  })
  if (query.data) {
    return (
      <ChannelAssetsForm
        config={query.data}
        channel={channel}
        onClose={onClose}
      />
    )
  }
  return (
    <Dialog
      open
      onOpenChange={(open) => !open && onClose()}
      title={t('Asset library')}
      description={channel.name}
      contentHeight='auto'
    >
      {query.isPending ? (
        <LoadingState />
      ) : (
        <ErrorState
          description={getServerErrorMessage(query.error)}
          onRetry={() => void query.refetch()}
        />
      )}
    </Dialog>
  )
}

function ChannelAssetsForm({
  config,
  channel,
  onClose,
}: {
  config: AssetChannelConfig
  channel: Channel
  onClose: () => void
}) {
  const { t } = useTranslation()
  const id = useId()
  const client = useQueryClient()
  const verification = useSecureVerification()
  const multiKey = isMultiKeyChannel(channel)
  const schema = assetChannelSchema(t, config.has_secret, multiKey)
  const { has_secret: _secret, channel_id: _channelId, ...defaults } = config
  const form = useForm<z.infer<typeof schema>>({
    resolver: zodResolver(schema),
    defaultValues: { ...defaults, secret_key: '' },
  })
  const authType = form.watch('auth_type')
  const mutation = useMutation({
    gcTime: 0,
    mutationFn: ({
      values,
      proof,
    }: {
      values: AssetChannelUpdate
      proof: string
    }) => saveAssetChannel(channel.id, values, proof),
    onSuccess: (saved) => {
      client.setQueryData(assetQueryKeys.channel(channel.id), saved)
      toast.success(t('Saved successfully'))
      onClose()
    },
    onError: (error) => handleServerError(error),
    onSettled: (): void => mutation.reset(),
  })
  const busy = mutation.isPending || verification.isActive
  const onSubmit = form.handleSubmit(async (values) => {
    try {
      const proof = await verification.requestVerification({
        scope: 'channel.assets.write',
        context: { channel_id: channel.id },
      })
      if (!proof) return
      const { secret_key, ...fields } = values
      mutation.mutate({
        values: {
          ...fields,
          ...(authType === 'aksk' && secret_key ? { secret_key } : {}),
        },
        proof: proof.proof_token,
      })
    } catch (error) {
      handleServerError(AuthOperationError.from(error))
    }
  })
  return (
    <>
      <Dialog
        open
        onOpenChange={(open) => !open && !busy && onClose()}
        title={t('Asset library')}
        description={channel.name}
        contentHeight='auto'
        footer={
          <>
            <Button variant='outline' onClick={onClose} disabled={busy}>
              {t('Cancel')}
            </Button>
            <Button
              form={id}
              type='submit'
              disabled={busy || !form.formState.isDirty}
            >
              {busy ? t('Saving...') : t('Save')}
            </Button>
          </>
        }
      >
        <Form {...form}>
          <form id={id} className='space-y-4' onSubmit={onSubmit}>
            {multiKey && (
              <Alert>
                <AlertDescription>
                  {t('Asset libraries require a single-key channel.')}
                </AlertDescription>
              </Alert>
            )}
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
                      disabled={busy || (multiKey && !field.value)}
                    />
                  </FormControl>
                  <FormMessage />
                </SettingsSwitchItem>
              )}
            />
            <FormField
              control={form.control}
              name='endpoint'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t('Asset API endpoint')}</FormLabel>
                  <FormControl>
                    <Input
                      {...field}
                      type='url'
                      disabled={busy}
                      placeholder='https://'
                    />
                  </FormControl>
                  <FormDescription>
                    {t(
                      'Use the upstream asset API endpoint, including its path if required.'
                    )}
                  </FormDescription>
                  <FormMessage />
                </FormItem>
              )}
            />
            <FormField
              control={form.control}
              name='project_name'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t('Upstream project name')}</FormLabel>
                  <FormControl>
                    <Input {...field} disabled={busy} />
                  </FormControl>
                  <FormMessage />
                </FormItem>
              )}
            />
            <FormField
              control={form.control}
              name='auth_type'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t('Authentication')}</FormLabel>
                  <FormControl>
                    <NativeSelect {...field} className='w-full' disabled={busy}>
                      <option value='bearer'>
                        {t('Channel key (Bearer)')}
                      </option>
                      <option value='aksk'>AK/SK</option>
                    </NativeSelect>
                  </FormControl>
                  <FormMessage />
                </FormItem>
              )}
            />
            {authType === 'bearer' ? (
              <p className='text-muted-foreground text-sm'>
                {t(
                  'Reuses this channel’s key. No additional credential is needed.'
                )}
              </p>
            ) : (
              <>
                <FormField
                  control={form.control}
                  name='access_key'
                  render={({ field }) => (
                    <FormItem>
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
              </>
            )}
          </form>
        </Form>
      </Dialog>
      <SecureVerificationDialog {...verification.dialogProps} />
    </>
  )
}
