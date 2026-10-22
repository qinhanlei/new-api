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
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { useFormContext, useWatch } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { ErrorState } from '@/components/error-state'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import {
  FieldDescription,
  FieldGroup,
  FieldLegend,
  FieldSet,
} from '@/components/ui/field'
import {
  FormControl,
  FormDescription,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { Input } from '@/components/ui/input'
import { RadioGroup, RadioGroupItem } from '@/components/ui/radio-group'
import { handleServerError } from '@/lib/handle-server-error'

import { cleanupChannelAssets, getChannelAssets } from '../api'
import {
  supportsChannelAssets,
  type ChannelFormValues,
} from '../lib/channel-form'

export function ChannelAssetSettings(props: {
  channelId: number | null
  channelEnabled?: boolean
  disabled?: boolean
}) {
  const { t } = useTranslation()
  const form = useFormContext<ChannelFormValues>()
  const [type, pluginKey, extensionKeys, authMode, configured] = useWatch({
    control: form.control,
    name: [
      'type',
      'task_plugin_key',
      'task_extend_plugin_keys',
      'asset_auth_mode',
      'asset_credentials_configured',
    ],
  })
  const supported = supportsChannelAssets({
    type,
    task_plugin_key: pluginKey,
    task_extend_plugin_keys: extensionKeys,
  })
  const queryClient = useQueryClient()
  const channelId = props.channelId
  const [confirmCleanup, setConfirmCleanup] = useState(false)
  const query = useQuery({
    queryKey: ['channel-assets', props.channelId],
    queryFn: () =>
      channelId ? getChannelAssets(channelId) : Promise.resolve(null),
    enabled: Boolean(supported && props.channelId),
    refetchInterval: (query) => (query.state.data ? 5000 : false),
  })
  const cleanup = useMutation({
    mutationFn: cleanupChannelAssets,
    onSuccess: () => {
      setConfirmCleanup(false)
      toast.success(t('Asset replica cleanup queued'))
      void queryClient.invalidateQueries({
        queryKey: ['channel-assets', props.channelId],
      })
    },
    onError: (error) => handleServerError(error),
  })
  if (!supported) return null
  const cleaning = query.data?.status === 'cleaning'
  return (
    <FieldSet className='rounded-lg border p-4' disabled={props.disabled}>
      <FieldLegend variant='label'>{t('Asset library')}</FieldLegend>
      <FieldDescription>
        {t(
          'Assets use this channel and are imported automatically when needed. Use one credential per channel.'
        )}
      </FieldDescription>
      <FieldGroup>
        <FormField
          control={form.control}
          name='asset_endpoint'
          render={({ field }) => (
            <FormItem>
              <FormLabel>{t('Asset management URL')}</FormLabel>
              <FormControl>
                <Input
                  {...field}
                  value={field.value || ''}
                  type='url'
                  placeholder='https://ark.cn-beijing.volcengineapi.com/'
                  disabled={props.disabled}
                />
              </FormControl>
              <FormDescription>
                {t(
                  'Leave blank for the official service. For a custom upstream, enter its official-compatible asset endpoint.'
                )}
              </FormDescription>
              <FormMessage />
            </FormItem>
          )}
        />
        <FormField
          control={form.control}
          name='asset_project'
          render={({ field }) => (
            <FormItem>
              <FormLabel>{t('Project')}</FormLabel>
              <FormControl>
                <Input
                  {...field}
                  value={field.value || ''}
                  placeholder='default'
                  disabled={props.disabled}
                />
              </FormControl>
              <FormMessage />
            </FormItem>
          )}
        />
        <FormField
          control={form.control}
          name='asset_auth_mode'
          render={({ field }) => (
            <FormItem>
              <FormLabel>{t('Asset credentials')}</FormLabel>
              <FormControl>
                <RadioGroup
                  aria-label={t('Asset credentials')}
                  value={field.value || 'channel'}
                  onValueChange={field.onChange}
                  disabled={props.disabled}
                  className='flex flex-wrap gap-4'
                >
                  <FormItem className='flex items-center gap-2 space-y-0'>
                    <FormControl>
                      <RadioGroupItem value='channel' />
                    </FormControl>
                    <FormLabel className='font-normal'>
                      {t('Channel API key')}
                    </FormLabel>
                  </FormItem>
                  <FormItem className='flex items-center gap-2 space-y-0'>
                    <FormControl>
                      <RadioGroupItem value='aksk' />
                    </FormControl>
                    <FormLabel className='font-normal'>AK/SK</FormLabel>
                  </FormItem>
                </RadioGroup>
              </FormControl>
              <FormDescription>
                {t(
                  'The official asset API requires AK/SK. Compatible services may accept the channel API key.'
                )}
              </FormDescription>
            </FormItem>
          )}
        />
        {authMode === 'aksk' && (
          <FieldGroup>
            {configured && (
              <FieldDescription>
                {t(
                  'Credentials configured. Leave both fields blank to keep them.'
                )}
              </FieldDescription>
            )}
            <FormField
              control={form.control}
              name='asset_access_key'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t('Access key')}</FormLabel>
                  <FormControl>
                    <Input
                      {...field}
                      value={field.value || ''}
                      type='password'
                      autoComplete='off'
                      disabled={props.disabled}
                    />
                  </FormControl>
                  <FormMessage />
                </FormItem>
              )}
            />
            <FormField
              control={form.control}
              name='asset_secret_key'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t('Secret key')}</FormLabel>
                  <FormControl>
                    <Input
                      {...field}
                      value={field.value || ''}
                      type='password'
                      autoComplete='new-password'
                      disabled={props.disabled}
                    />
                  </FormControl>
                  <FormMessage />
                </FormItem>
              )}
            />
          </FieldGroup>
        )}
        {authMode === 'channel' && configured && (
          <FieldDescription>
            {t('Saving replaces the stored AK/SK with the channel API key.')}
          </FieldDescription>
        )}
        {query.isError && (
          <ErrorState
            onRetry={() => {
              void query.refetch()
            }}
          />
        )}
        {query.data && (
          <>
            {(cleaning || query.data.error) && (
              <Alert>
                <AlertDescription role='status'>
                  {cleaning
                    ? t(
                        'Cleaning channel replicas. Original assets remain available.'
                      )
                    : t(query.data.error || '')}
                </AlertDescription>
              </Alert>
            )}
            {props.channelEnabled && (
              <FieldDescription>
                {t('Disable the channel before cleaning its assets')}
              </FieldDescription>
            )}
            <Button
              type='button'
              variant='outline'
              className='w-fit'
              disabled={
                props.disabled ||
                props.channelEnabled ||
                cleaning ||
                cleanup.isPending
              }
              onClick={() => setConfirmCleanup(true)}
            >
              {t('Clean up channel replicas')}
            </Button>
          </>
        )}
      </FieldGroup>
      <ConfirmDialog
        open={confirmCleanup}
        onOpenChange={setConfirmCleanup}
        title={t('Clean up channel replicas')}
        desc={t(
          'Channel replicas will be deleted. Original assets and replicas on other channels are kept.'
        )}
        confirmText={t('Clean up')}
        destructive
        disabled={props.disabled}
        isLoading={cleanup.isPending}
        handleConfirm={() => {
          if (channelId) cleanup.mutate(channelId)
        }}
      />
    </FieldSet>
  )
}
