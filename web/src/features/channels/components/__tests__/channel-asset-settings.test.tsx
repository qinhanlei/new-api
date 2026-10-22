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
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useForm } from 'react-hook-form'
import { describe, expect, test, vi } from 'vitest'

import { Button } from '@/components/ui/button'
import { Form } from '@/components/ui/form'
import { api } from '@/lib/api'

import {
  CHANNEL_FORM_DEFAULT_VALUES,
  channelFormSchema,
  transformChannelToFormDefaults,
  transformFormDataToCreatePayload,
  transformFormDataToUpdatePayload,
  type ChannelFormValues,
} from '../../lib/channel-form'
import { channelSchema, type ChannelWrite } from '../../types'
import { ChannelAssetSettings } from '../channel-asset-settings'

const defaults: ChannelFormValues = {
  ...CHANNEL_FORM_DEFAULT_VALUES,
  name: 'Video channel',
  type: 54,
  models: 'video-model',
  asset_endpoint: 'https://upstream.example/assets/',
}

function SettingsForm(props: {
  disabled?: boolean
  configured?: boolean
  channelId?: number
  channelEnabled?: boolean
  allowProviderChange?: boolean
  onSave: (payload: ChannelWrite) => void
}) {
  const form = useForm<ChannelFormValues>({
    resolver: zodResolver(channelFormSchema),
    defaultValues: {
      ...defaults,
      asset_auth_mode: props.configured ? 'aksk' : 'channel',
      asset_credentials_configured: props.configured,
    },
  })
  return (
    <Form {...form}>
      <form
        onSubmit={form.handleSubmit((value) =>
          props.onSave(transformFormDataToUpdatePayload(value, 1))
        )}
      >
        <ChannelAssetSettings
          channelId={props.channelId ?? null}
          channelEnabled={props.channelEnabled}
          disabled={props.disabled}
        />
        {props.allowProviderChange && (
          <Button
            type='button'
            onClick={() => form.setValue('type', 1, { shouldValidate: true })}
          >
            Use OpenAI
          </Button>
        )}
        <Button type='submit'>Save</Button>
      </form>
    </Form>
  )
}

function renderSettings(props: React.ComponentProps<typeof SettingsForm>) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  return render(
    <QueryClientProvider client={client}>
      <SettingsForm {...props} />
    </QueryClientProvider>
  )
}

describe('channel asset settings', () => {
  test('saving and reloading preserves the endpoint without returning stored credentials to the form', () => {
    const payload = transformFormDataToCreatePayload({
      ...defaults,
      asset_auth_mode: 'aksk',
      asset_access_key: 'test-access',
      asset_secret_key: 'test-secret',
    }).channel
    expect(payload.asset_credentials).toEqual({
      access_key: 'test-access',
      secret_key: 'test-secret',
    })
    expect(JSON.parse(payload.setting ?? '{}')).toMatchObject({
      asset_endpoint: 'https://upstream.example/assets/',
    })
    expect(payload.setting).not.toContain('test-secret')
    const channel = channelSchema.parse({
      id: 1,
      name: 'video',
      key: '',
      type: 54,
      status: 1,
      created_time: 0,
      test_time: 0,
      response_time: 0,
      balance_updated_time: 0,
      setting: payload.setting,
      asset_credentials_configured: true,
    })
    const values = transformChannelToFormDefaults(channel)
    expect(values.asset_endpoint).toBe(defaults.asset_endpoint)
    expect(values.asset_auth_mode).toBe('aksk')
    expect(values.asset_access_key).toBe('')
    expect(values.asset_secret_key).toBe('')
    expect(
      transformFormDataToUpdatePayload(values, 1).asset_credentials
    ).toBeUndefined()
  })

  test('switching configured credentials to the channel key sends an explicit clear', async () => {
    const onSave = vi.fn()
    renderSettings({ configured: true, onSave })
    expect(
      screen.getByRole('radiogroup', { name: 'Asset credentials' })
    ).toBeVisible()
    await userEvent.click(
      screen.getByRole('radio', { name: 'Channel API key' })
    )
    expect(
      screen.getByText(
        'Saving replaces the stored AK/SK with the channel API key.'
      )
    ).toBeVisible()
    await userEvent.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(onSave).toHaveBeenCalled())
    expect(onSave.mock.calls[0][0].asset_credentials).toEqual({})
  })

  test('a partial credential change reports the missing secret and does not save', async () => {
    const onSave = vi.fn()
    renderSettings({ configured: true, onSave })
    await userEvent.type(
      screen.getByLabelText('Access key'),
      'replacement-access'
    )
    await userEvent.click(screen.getByRole('button', { name: 'Save' }))
    expect(
      await screen.findByText('Enter both access key and secret key')
    ).toBeVisible()
    expect(screen.getByLabelText('Secret key')).toHaveAttribute(
      'aria-invalid',
      'true'
    )
    expect(onSave).not.toHaveBeenCalled()
  })

  test('switching an incomplete asset draft to OpenAI hides it and saves without asset settings', async () => {
    const user = userEvent.setup()
    const onSave = vi.fn()
    renderSettings({ allowProviderChange: true, onSave })
    await user.click(screen.getByRole('radio', { name: 'AK/SK' }))
    await user.type(screen.getByLabelText('Access key'), 'draft-access')
    await user.clear(screen.getByLabelText('Asset management URL'))
    await user.type(
      screen.getByLabelText('Asset management URL'),
      'http://upstream.example/invalid'
    )
    await user.type(screen.getByLabelText('Project'), 'draft-project')
    await user.click(screen.getByRole('button', { name: 'Use OpenAI' }))
    expect(
      screen.queryByRole('group', { name: 'Asset library' })
    ).not.toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(onSave).toHaveBeenCalled())
    const payload = onSave.mock.calls[0][0]
    expect(payload.asset_credentials).toBeUndefined()
    expect(JSON.parse(payload.setting)).not.toHaveProperty('asset_endpoint')
    expect(JSON.parse(payload.setting)).not.toHaveProperty('asset_project')
  })

  test('switching a configured channel to OpenAI explicitly clears asset credentials for backend scope validation', () => {
    const values = {
      ...defaults,
      type: 1,
      asset_auth_mode: 'aksk' as const,
      asset_credentials_configured: true,
    }
    const parsed = channelFormSchema.parse(values)
    const payload = transformFormDataToUpdatePayload(parsed, 1)
    expect(payload).toMatchObject({ id: 1, type: 1, asset_credentials: {} })
    expect(JSON.parse(payload.setting ?? '{}')).not.toHaveProperty(
      'asset_endpoint'
    )
  })

  test('unsupported task plugins ignore asset drafts while Doubao retains their validation', () => {
    const values = {
      ...defaults,
      type: 61,
      base_url: 'https://upstream.example',
      task_plugin_key: 'other-task',
      asset_auth_mode: 'aksk' as const,
      asset_access_key: 'draft-access',
      asset_endpoint: 'http://invalid.example/',
    }
    expect(channelFormSchema.safeParse(values).success).toBe(true)
    const payload = transformFormDataToCreatePayload(values).channel
    expect(payload.asset_credentials).toBeUndefined()
    expect(JSON.parse(payload.setting ?? '{}')).not.toHaveProperty(
      'asset_endpoint'
    )
    expect(
      channelFormSchema.safeParse({ ...values, task_plugin_key: 'doubao' })
        .success
    ).toBe(false)
  })

  test('removing the Doubao extension ignores a stale task plugin key when saving New API channels', () => {
    const values = {
      ...defaults,
      type: 60,
      base_url: 'https://upstream.example',
      task_plugin_key: 'doubao',
      task_extend_plugin_keys: [],
      asset_auth_mode: 'aksk' as const,
      asset_access_key: 'draft-access',
    }
    expect(channelFormSchema.safeParse(values).success).toBe(true)
    const payload = transformFormDataToCreatePayload(values).channel
    const settings = JSON.parse(payload.setting ?? '{}')
    expect(settings).not.toHaveProperty('task_plugin_key')
    expect(settings).not.toHaveProperty('asset_endpoint')
    expect(payload.asset_credentials).toBeUndefined()
    expect(
      channelFormSchema.safeParse({
        ...values,
        task_extend_plugin_keys: ['doubao'],
      }).success
    ).toBe(false)
  })

  test('locked sensitive settings disable endpoint and credentials', () => {
    renderSettings({ configured: true, disabled: true, onSave: vi.fn() })
    expect(screen.getByLabelText('Asset management URL')).toBeDisabled()
    expect(screen.getByLabelText('Access key')).toBeDisabled()
    expect(screen.getByLabelText('Secret key')).toBeDisabled()
    expect(
      screen.getByRole('radio', { name: 'Channel API key' })
    ).toHaveAttribute('aria-disabled', 'true')
  })

  test('confirmed cleanup queues deletion and shows ongoing cleanup without removing original assets', async () => {
    const get = vi.spyOn(api, 'get').mockResolvedValue({
      data: {
        success: true,
        data: { id: 'scope', channel_id: 1, status: 'ready' },
      },
    })
    const post = vi.spyOn(api, 'post').mockImplementation(async () => {
      get.mockResolvedValue({
        data: {
          success: true,
          data: { id: 'scope', channel_id: 1, status: 'cleaning' },
        },
      })
      return { data: { success: true, data: { status: 'cleaning' } } }
    })
    renderSettings({ channelId: 1, onSave: vi.fn() })
    await userEvent.click(
      await screen.findByRole('button', { name: 'Clean up channel replicas' })
    )
    expect(
      screen.getByText(
        'Channel replicas will be deleted. Original assets and replicas on other channels are kept.'
      )
    ).toBeVisible()
    await userEvent.click(screen.getByRole('button', { name: 'Clean up' }))
    expect(
      await screen.findByText(
        'Cleaning channel replicas. Original assets remain available.'
      )
    ).toBeVisible()
    expect(
      screen.getByRole('button', { name: 'Clean up channel replicas' })
    ).toBeDisabled()
    expect(post).toHaveBeenCalledWith(
      '/api/channel/1/assets/cleanup',
      {},
      expect.any(Object)
    )
  })

  test('enabled channels must be disabled before cleaning replicas', async () => {
    vi.spyOn(api, 'get').mockResolvedValue({
      data: {
        success: true,
        data: { id: 'scope', channel_id: 1, status: 'ready' },
      },
    })
    renderSettings({ channelId: 1, channelEnabled: true, onSave: vi.fn() })
    expect(
      await screen.findByText('Disable the channel before cleaning its assets')
    ).toBeVisible()
    expect(
      screen.getByRole('button', { name: 'Clean up channel replicas' })
    ).toBeDisabled()
  })

  test('a failed cleanup remains open and allows retry', async () => {
    vi.spyOn(api, 'get').mockResolvedValue({
      data: {
        success: true,
        data: { id: 'scope', channel_id: 1, status: 'ready' },
      },
    })
    const post = vi.spyOn(api, 'post').mockResolvedValue({
      data: { success: false, message: 'Asset cleanup failed' },
    })
    renderSettings({ channelId: 1, onSave: vi.fn() })
    await userEvent.click(
      await screen.findByRole('button', { name: 'Clean up channel replicas' })
    )
    await userEvent.click(screen.getByRole('button', { name: 'Clean up' }))
    await waitFor(() => expect(post).toHaveBeenCalled())
    await waitFor(() =>
      expect(screen.getByRole('button', { name: 'Clean up' })).toBeEnabled()
    )
    expect(screen.getByRole('alertdialog')).toBeVisible()
  })
})
