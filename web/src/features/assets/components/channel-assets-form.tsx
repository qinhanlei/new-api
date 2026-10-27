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
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useId, useState } from 'react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import type { z } from 'zod'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { Dialog } from '@/components/dialog'
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
import { assetQueryKeys, saveAssetChannel } from '@/features/assets/api'
import { assetChannelSchema } from '@/features/assets/lib/schemas'
import type {
  AssetChannelConfig,
  AssetChannelUpdate,
} from '@/features/assets/types'
import {
  SecureVerificationDialog,
  useSecureVerification,
} from '@/features/auth/secure-verification'
import { isMultiKeyChannel } from '@/features/channels/lib'
import type { Channel } from '@/features/channels/types'
import {
  SettingsSwitchContent,
  SettingsSwitchItem,
} from '@/features/system-settings/components/settings-form-layout'
import { handleServerError } from '@/lib/handle-server-error'
import { AuthOperationError } from '@/lib/secure-verification'

import {
  AssetChannelSelect,
  type AssetChannelSearch,
} from './asset-channel-select'

export function ChannelAssetsForm(props: {
  config: AssetChannelConfig
  channel: Channel
  onChannelChange: (channel: Channel) => void
  search: AssetChannelSearch
  onSearchChange: (search: AssetChannelSearch) => void
  onClose: () => void
}) {
  const { config, channel } = props
  const [pendingChange, setPendingChange] = useState<Channel | 'close' | null>(
    null
  )
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
      const {
        has_secret: _savedSecret,
        channel_id: _savedChannel,
        ...values
      } = saved
      form.reset({ ...values, secret_key: '' })
    },
    onError: (error) => handleServerError(error),
    onSettled: (): void => mutation.reset(),
  })
  const busy =
    mutation.isPending || verification.isActive || form.formState.isSubmitting
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
  const requestChange = (target: Channel | 'close') => {
    if (busy || (target !== 'close' && target.id === channel.id)) return
    if (form.formState.isDirty) {
      setPendingChange(target)
      return
    }
    if (target === 'close') props.onClose()
    else props.onChannelChange(target)
  }
  return (
    <>
      <Dialog
        open
        onOpenChange={(open) => !open && requestChange('close')}
        title={t('Channel settings')}
        contentHeight='auto'
        footer={
          <>
            <Button
              variant='outline'
              onClick={() => requestChange('close')}
              disabled={busy}
            >
              {t('Close')}
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
        <div className='space-y-4'>
          <AssetChannelSelect
            channel={channel}
            onChange={requestChange}
            search={props.search}
            onSearchChange={props.onSearchChange}
            disabled={busy}
          />
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
                      <NativeSelect
                        {...field}
                        className='w-full'
                        disabled={busy}
                      >
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
                          <Input
                            {...field}
                            disabled={busy}
                            autoComplete='off'
                          />
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
        </div>
      </Dialog>
      <ConfirmDialog
        open={pendingChange !== null}
        onOpenChange={(open) => !open && setPendingChange(null)}
        title={t('Unsaved changes')}
        desc={t('You have unsaved changes. Are you sure you want to leave?')}
        confirmText={t('Discard changes')}
        handleConfirm={() => {
          const target = pendingChange
          setPendingChange(null)
          if (target === 'close') props.onClose()
          else if (target) props.onChannelChange(target)
        }}
      />
      <SecureVerificationDialog {...verification.dialogProps} />
    </>
  )
}
