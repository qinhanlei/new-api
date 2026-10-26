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
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'

import { ChannelAssetsDialog } from '@/features/channels/components/dialogs/channel-assets-dialog'
import { channelSchema } from '@/features/channels/types'
import { SettingsPageProvider } from '@/features/system-settings/components/settings-page-context'
import { AssetStorageSection } from '@/features/system-settings/operations/asset-storage-section'
import { api } from '@/lib/api'

import type { AssetStorageConfig } from '../types'

const config: AssetStorageConfig = {
  enabled: true,
  endpoint: 'https://s3.example.com',
  region: 'cn-beijing',
  bucket: 'assets',
  access_key: 'access-id',
  force_path_style: false,
  revision: 2,
  has_secret: true,
}
const channel = channelSchema.parse({
  id: 7,
  type: 54,
  key: '',
  status: 1,
  name: 'Video channel',
  created_time: 0,
  test_time: 0,
  response_time: 0,
  balance_updated_time: 0,
})
let client: QueryClient

function StorageFixture() {
  const [container, setContainer] = useState<HTMLDivElement | null>(null)
  return (
    <>
      <div ref={setContainer} />
      <SettingsPageProvider actionsContainer={container}>
        <AssetStorageSection />
      </SettingsPageProvider>
    </>
  )
}

beforeEach(() => {
  client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  vi.spyOn(api, 'get').mockImplementation(async (url, options) => {
    if (url === '/api/verify/methods') {
      return {
        data: {
          success: true,
          data: {
            scope: (options?.params as { scope?: string } | undefined)?.scope,
            methods: [{ method: 'session', available: true }],
            oauth_providers: [],
            password_encryption_enabled: false,
          },
        },
      }
    }
    if (url === '/api/option/asset_storage') {
      return { data: { success: true, data: config } }
    }
    return {
      data: {
        success: true,
        data: {
          channel_id: 7,
          enabled: false,
          endpoint: 'https://upstream.example.com',
          project_name: 'default',
          auth_type: 'bearer',
          access_key: '',
          has_secret: false,
          revision: 0,
        },
      },
    }
  })
  vi.spyOn(api, 'post').mockImplementation(async (url, body) => ({
    data: {
      success: true,
      data:
        url === '/api/verify'
          ? {
              proof_token: 'single-use-proof',
              scope: (body as { scope: string }).scope,
              method: 'session',
              expires_at: Math.floor(Date.now() / 1000) + 60,
            }
          : {},
    },
  }))
})
afterEach(() => client.clear())

test('saving storage reuses verification and preserves a saved secret when blank', async () => {
  const user = userEvent.setup()
  const put = vi.spyOn(api, 'put').mockResolvedValue({
    data: {
      success: true,
      data: { ...config, bucket: 'new-assets', revision: 3 },
    },
  })
  render(
    <QueryClientProvider client={client}>
      <StorageFixture />
    </QueryClientProvider>
  )
  const bucket = await screen.findByLabelText('Bucket')
  expect(screen.getByLabelText('Secret key')).toHaveValue('')
  await user.clear(bucket)
  await user.type(bucket, 'new-assets')
  await user.click(screen.getByRole('button', { name: 'Save Changes' }))
  await waitFor(() => expect(put).toHaveBeenCalledOnce())
  expect(put).toHaveBeenCalledWith(
    '/api/option/asset_storage',
    {
      enabled: true,
      endpoint: config.endpoint,
      region: config.region,
      bucket: 'new-assets',
      access_key: config.access_key,
      force_path_style: false,
      revision: 2,
    },
    expect.objectContaining({
      headers: { 'X-Security-Proof': 'single-use-proof' },
      singleUseAuthorization: true,
    })
  )
  expect(api.post).toHaveBeenCalledWith(
    '/api/verify',
    { method: 'session', scope: 'asset.storage.write' },
    expect.anything()
  )
  await waitFor(() =>
    expect(screen.getByRole('button', { name: 'Save Changes' })).toBeDisabled()
  )
})

test('connection testing uses its own proof and keeps unsaved configuration', async () => {
  const user = userEvent.setup()
  render(
    <QueryClientProvider client={client}>
      <StorageFixture />
    </QueryClientProvider>
  )
  await screen.findByLabelText('Bucket')
  await user.type(screen.getByLabelText('Secret key'), 'replacement-secret')
  await user.click(screen.getByRole('button', { name: 'Test Connection' }))
  await waitFor(() =>
    expect(api.post).toHaveBeenCalledWith(
      '/api/option/asset_storage/test',
      expect.objectContaining({
        secret_key: 'replacement-secret',
        revision: 2,
      }),
      expect.objectContaining({ singleUseAuthorization: true })
    )
  )
  expect(api.post).toHaveBeenCalledWith(
    '/api/verify',
    { method: 'session', scope: 'asset.storage.test' },
    expect.anything()
  )
  expect(screen.getByLabelText('Secret key')).toHaveValue('replacement-secret')
  expect(screen.getByRole('button', { name: 'Save Changes' })).toBeEnabled()
})

test('a cancelled verification never saves credentials and keeps the form editable', async () => {
  const user = userEvent.setup()
  vi.mocked(api.get).mockImplementation(async (url) => ({
    data: {
      success: true,
      data:
        url === '/api/verify/methods'
          ? {
              scope: 'asset.storage.write',
              methods: [{ method: 'password', available: true }],
              oauth_providers: [],
              password_encryption_enabled: false,
            }
          : config,
    },
  }))
  const put = vi.spyOn(api, 'put')
  render(
    <QueryClientProvider client={client}>
      <StorageFixture />
    </QueryClientProvider>
  )
  await user.type(await screen.findByLabelText('Bucket'), '-new')
  await user.click(screen.getByRole('button', { name: 'Save Changes' }))
  const dialog = await screen.findByRole('dialog', {
    name: 'Security verification',
  })
  await user.click(within(dialog).getByRole('button', { name: 'Cancel' }))
  expect(put).not.toHaveBeenCalled()
  expect(screen.getByLabelText('Bucket')).toHaveValue('assets-new')
  expect(screen.getByRole('button', { name: 'Save Changes' })).toBeEnabled()
})

test('single-key channel uses its existing key and binds verification to the channel ID', async () => {
  const user = userEvent.setup()
  const close = vi.fn()
  const put = vi
    .spyOn(api, 'put')
    .mockResolvedValue({ data: { success: true, data: {} } })
  render(
    <QueryClientProvider client={client}>
      <ChannelAssetsDialog channel={channel} onClose={close} />
    </QueryClientProvider>
  )
  await user.click(
    await screen.findByRole('switch', { name: 'Enable asset library' })
  )
  expect(screen.queryByLabelText('Secret key')).not.toBeInTheDocument()
  await user.click(screen.getByRole('button', { name: 'Save' }))
  await waitFor(() => expect(close).toHaveBeenCalledOnce())
  expect(api.post).toHaveBeenCalledWith(
    '/api/verify',
    {
      method: 'session',
      scope: 'channel.assets.write',
      context: { channel_id: 7 },
    },
    expect.anything()
  )
  expect(put).toHaveBeenCalledWith(
    '/api/channel/7/assets',
    {
      enabled: true,
      endpoint: 'https://upstream.example.com',
      project_name: 'default',
      auth_type: 'bearer',
      access_key: '',
      revision: 0,
    },
    expect.objectContaining({ singleUseAuthorization: true })
  )
})

test('multi-key channels cannot enable assets', async () => {
  render(
    <QueryClientProvider client={client}>
      <ChannelAssetsDialog
        channel={{
          ...channel,
          channel_info: {
            ...channel.channel_info,
            is_multi_key: true,
            multi_key_size: 2,
          },
        }}
        onClose={vi.fn()}
      />
    </QueryClientProvider>
  )
  expect(
    await screen.findByRole('switch', { name: 'Enable asset library' })
  ).toHaveAttribute('aria-disabled', 'true')
  expect(screen.getByRole('alert')).toHaveTextContent(
    'Asset libraries require a single-key channel.'
  )
})

test('storage errors preserve unsaved secrets and an invalid endpoint prevents verification', async () => {
  const user = userEvent.setup()
  const put = vi.spyOn(api, 'put').mockResolvedValue({
    data: {
      success: false,
      message: 'Asset settings changed. Reload and try again.',
    },
  })
  render(
    <QueryClientProvider client={client}>
      <StorageFixture />
    </QueryClientProvider>
  )
  const endpoint = await screen.findByLabelText('S3 endpoint')
  await user.clear(endpoint)
  await user.type(endpoint, 'http://insecure.example.com')
  await user.click(screen.getByRole('button', { name: 'Save Changes' }))
  expect(await screen.findByText('Enter an HTTPS URL')).toBeVisible()
  expect(api.post).not.toHaveBeenCalled()
  await user.clear(endpoint)
  await user.type(endpoint, 'https://replacement.example.com')
  await user.type(screen.getByLabelText('Secret key'), 'new-secret')
  await user.click(screen.getByRole('button', { name: 'Save Changes' }))
  await waitFor(() => expect(put).toHaveBeenCalledOnce())
  await waitFor(() =>
    expect(screen.getByRole('button', { name: 'Save Changes' })).toBeEnabled()
  )
  expect(screen.getByLabelText('Secret key')).toHaveValue('new-secret')
  expect(endpoint).toHaveValue('https://replacement.example.com')
})
