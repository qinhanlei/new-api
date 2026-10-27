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
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'

import { channelSchema } from '@/features/channels/types'
import { SettingsPageProvider } from '@/features/system-settings/components/settings-page-context'
import { AssetStorageSection } from '@/features/system-settings/operations/asset-storage-section'
import { api } from '@/lib/api'

import { AssetChannelSettingsDialog } from '../components/asset-channel-settings-dialog'
import type { AssetChannelConfig, AssetStorageConfig } from '../types'

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
  type: 45,
  key: '',
  status: 1,
  name: 'Video channel',
  created_time: 0,
  test_time: 0,
  response_time: 0,
  balance_updated_time: 0,
})
const otherChannel = { ...channel, id: 8, name: 'Second channel' }
let channels = [channel, otherChannel]
let channelConfigs: Record<number, AssetChannelConfig>
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
  channels = [channel, otherChannel]
  channelConfigs = {
    7: {
      channel_id: 7,
      enabled: false,
      endpoint: 'https://upstream.example.com',
      project_name: 'default',
      auth_type: 'bearer',
      access_key: '',
      has_secret: false,
      revision: 0,
    },
    8: {
      channel_id: 8,
      enabled: true,
      endpoint: 'https://second.example.com/assets',
      project_name: 'second',
      auth_type: 'aksk',
      access_key: 'second-access',
      has_secret: true,
      revision: 3,
    },
  }
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
    if (url === '/api/channel' || url === '/api/channel/search') {
      const type = (options?.params as { type?: number })?.type
      const keyword = String(
        (options?.params as { keyword?: string })?.keyword ?? ''
      )
      const items = channels.filter(
        (item) =>
          (type === undefined || item.type === type) &&
          (item.name.toLowerCase().includes(keyword.toLowerCase()) ||
            String(item.id) === keyword)
      )
      return { data: { success: true, data: { items, total: items.length } } }
    }
    const id = Number(String(url).split('/')[3])
    return { data: { success: true, data: channelConfigs[id] } }
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

test('saving storage needs no verification and preserves a saved secret when blank', async () => {
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
      provider: 'other',
      endpoint: config.endpoint,
      region: config.region,
      bucket: 'new-assets',
      access_key: config.access_key,
      force_path_style: false,
      revision: 2,
    },
    expect.not.objectContaining({ singleUseAuthorization: true })
  )
  expect(api.post).not.toHaveBeenCalled()
  expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  await waitFor(() =>
    expect(screen.getByRole('button', { name: 'Save Changes' })).toBeDisabled()
  )
})

test('connection testing needs no verification and keeps unsaved configuration', async () => {
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
      expect.not.objectContaining({ singleUseAuthorization: true })
    )
  )
  expect(api.post).toHaveBeenCalledOnce()
  expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  expect(screen.getByLabelText('Secret key')).toHaveValue('replacement-secret')
  expect(screen.getByRole('button', { name: 'Save Changes' })).toBeEnabled()
})

test('selecting a channel fetches only its settings and saves with verification bound to its ID', async () => {
  const user = userEvent.setup()
  const close = vi.fn()
  const put = vi.spyOn(api, 'put').mockResolvedValue({
    data: {
      success: true,
      data: { ...channelConfigs[7], enabled: true, revision: 1 },
    },
  })
  render(
    <QueryClientProvider client={client}>
      <AssetChannelSettingsDialog onClose={close} />
    </QueryClientProvider>
  )
  await screen.findByRole('option', { name: 'Video channel (#7)' })
  expect(api.get).not.toHaveBeenCalledWith(
    '/api/channel/7/assets',
    expect.anything()
  )
  await user.selectOptions(screen.getByLabelText('Channel'), '7')
  await user.click(
    await screen.findByRole('switch', { name: 'Enable asset library' })
  )
  expect(screen.queryByLabelText('Secret key')).not.toBeInTheDocument()
  expect(api.get).not.toHaveBeenCalledWith(
    '/api/channel/8/assets',
    expect.anything()
  )
  await user.click(screen.getByRole('button', { name: 'Save' }))
  await waitFor(() => expect(put).toHaveBeenCalledOnce())
  expect(close).not.toHaveBeenCalled()
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
    expect.objectContaining({
      singleUseAuthorization: true,
      headers: { 'X-Security-Proof': 'single-use-proof' },
    })
  )
  await waitFor(() =>
    expect(screen.getByRole('button', { name: 'Save' })).toBeDisabled()
  )
})

test('switching a dirty channel requires confirmation and clears the previous credentials', async () => {
  const user = userEvent.setup()
  channelConfigs[7] = {
    ...channelConfigs[7],
    auth_type: 'aksk',
    access_key: 'first-access',
    has_secret: true,
  }
  render(
    <QueryClientProvider client={client}>
      <AssetChannelSettingsDialog onClose={vi.fn()} />
    </QueryClientProvider>
  )
  await screen.findByRole('option', { name: 'Video channel (#7)' })
  await user.selectOptions(screen.getByLabelText('Channel'), '7')
  await user.type(await screen.findByLabelText('Secret key'), 'first-secret')
  await user.selectOptions(screen.getByLabelText('Channel'), '8')
  expect(await screen.findByRole('alertdialog')).toHaveTextContent(
    'Unsaved changes'
  )
  await user.click(screen.getByRole('button', { name: 'Cancel' }))
  expect(screen.getByLabelText('Channel')).toHaveValue('7')
  expect(screen.getByLabelText('Secret key')).toHaveValue('first-secret')
  await user.selectOptions(screen.getByLabelText('Channel'), '8')
  await user.click(
    await screen.findByRole('button', { name: 'Discard changes' })
  )
  expect(
    await screen.findByDisplayValue('https://second.example.com/assets')
  ).toBeVisible()
  expect(screen.getByLabelText('Secret key')).toHaveValue('')
  expect(screen.getByLabelText('Access key')).toHaveValue('second-access')
  expect(screen.getByRole('button', { name: 'Save' })).toBeDisabled()
})

test('channel settings can search by name or ID without fetching each result configuration', async () => {
  const user = userEvent.setup()
  render(
    <QueryClientProvider client={client}>
      <AssetChannelSettingsDialog onClose={vi.fn()} />
    </QueryClientProvider>
  )
  await screen.findByRole('option', { name: 'Video channel (#7)' })
  await user.type(
    screen.getByRole('searchbox', { name: 'Search channels' }),
    '8'
  )
  await user.click(screen.getByRole('button', { name: 'Search' }))
  await waitFor(() =>
    expect(
      screen.queryByRole('option', { name: 'Video channel (#7)' })
    ).not.toBeInTheDocument()
  )
  expect(
    screen.getByRole('option', { name: 'Second channel (#8)' })
  ).toBeInTheDocument()
  expect(api.get).toHaveBeenCalledWith(
    '/api/channel/search',
    expect.objectContaining({
      params: expect.objectContaining({ keyword: '8', p: 1, page_size: 20 }),
    })
  )
  expect(api.get).not.toHaveBeenCalledWith(
    '/api/channel/8/assets',
    expect.anything()
  )
})

test('switching search results preserves the search draft and applied keyword while clearing credentials', async () => {
  const user = userEvent.setup()
  channels = [channel, otherChannel, { ...channel, id: 9, name: 'Unrelated' }]
  channelConfigs[7] = {
    ...channelConfigs[7],
    auth_type: 'aksk',
    access_key: 'first-access',
    has_secret: true,
  }
  render(
    <QueryClientProvider client={client}>
      <AssetChannelSettingsDialog onClose={vi.fn()} />
    </QueryClientProvider>
  )
  await screen.findByRole('option', { name: 'Unrelated (#9)' })
  await user.type(
    screen.getByRole('searchbox', { name: 'Search channels' }),
    'channel'
  )
  await user.click(screen.getByRole('button', { name: 'Search' }))
  await waitFor(() =>
    expect(
      screen.queryByRole('option', { name: 'Unrelated (#9)' })
    ).not.toBeInTheDocument()
  )
  await user.type(
    screen.getByRole('searchbox', { name: 'Search channels' }),
    '-draft'
  )
  await user.selectOptions(screen.getByLabelText('Channel'), '7')
  await user.type(await screen.findByLabelText('Secret key'), 'first-secret')
  expect(
    screen.getByRole('searchbox', { name: 'Search channels' })
  ).toHaveValue('channel-draft')
  expect(
    screen.queryByRole('option', { name: 'Unrelated (#9)' })
  ).not.toBeInTheDocument()
  await user.selectOptions(screen.getByLabelText('Channel'), '8')
  await user.click(
    await screen.findByRole('button', { name: 'Discard changes' })
  )
  await screen.findByDisplayValue('https://second.example.com/assets')
  expect(
    screen.getByRole('searchbox', { name: 'Search channels' })
  ).toHaveValue('channel-draft')
  expect(
    screen.queryByRole('option', { name: 'Unrelated (#9)' })
  ).not.toBeInTheDocument()
  expect(
    screen.getByRole('option', { name: 'Video channel (#7)' })
  ).toBeInTheDocument()
  expect(screen.getByLabelText('Secret key')).toHaveValue('')
  expect(screen.getByRole('button', { name: 'Save' })).toBeDisabled()
})

test('channel listing and search only show Volcengine Ark channels', async () => {
  const user = userEvent.setup()
  channels = [
    channel,
    { ...otherChannel, type: 1, name: 'OpenAI channel' },
    { ...otherChannel, id: 9, type: 54, name: 'Doubao video channel' },
  ]
  render(
    <QueryClientProvider client={client}>
      <AssetChannelSettingsDialog onClose={vi.fn()} />
    </QueryClientProvider>
  )
  await screen.findByRole('option', { name: 'Video channel (#7)' })
  expect(
    screen.queryByRole('option', { name: /OpenAI/ })
  ).not.toBeInTheDocument()
  expect(
    screen.queryByRole('option', { name: /Doubao/ })
  ).not.toBeInTheDocument()
  await user.type(
    screen.getByRole('searchbox', { name: 'Search channels' }),
    'channel'
  )
  await user.click(screen.getByRole('button', { name: 'Search' }))
  await waitFor(() =>
    expect(api.get).toHaveBeenCalledWith(
      '/api/channel/search',
      expect.objectContaining({
        params: expect.objectContaining({ keyword: 'channel', type: 45 }),
      })
    )
  )
  expect(
    screen.getByRole('option', { name: 'Video channel (#7)' })
  ).toBeVisible()
  expect(
    screen.queryByRole('option', { name: /OpenAI/ })
  ).not.toBeInTheDocument()
  expect(
    screen.queryByRole('option', { name: /Doubao/ })
  ).not.toBeInTheDocument()
})

test('a failed channel list can be retried and no Ark channels cannot submit a configuration', async () => {
  const user = userEvent.setup()
  channels = [{ ...channel, type: 54 }]
  vi.mocked(api.get).mockRejectedValueOnce(new Error('Channels unavailable'))
  render(
    <QueryClientProvider client={client}>
      <AssetChannelSettingsDialog onClose={vi.fn()} />
    </QueryClientProvider>
  )
  expect(await screen.findByText('Channels unavailable')).toBeVisible()
  await user.click(screen.getByRole('button', { name: 'Retry' }))
  expect(await screen.findByText('No Channels Found')).toBeVisible()
  expect(screen.getByLabelText('Channel')).toHaveValue('')
  expect(screen.queryByRole('button', { name: 'Save' })).not.toBeInTheDocument()
})

test('multi-key channels cannot enable assets', async () => {
  const user = userEvent.setup()
  channels = [
    {
      ...channel,
      channel_info: {
        ...channel.channel_info,
        is_multi_key: true,
        multi_key_size: 2,
      },
    },
  ]
  render(
    <QueryClientProvider client={client}>
      <AssetChannelSettingsDialog onClose={vi.fn()} />
    </QueryClientProvider>
  )
  await screen.findByRole('option', { name: 'Video channel (#7)' })
  await user.selectOptions(screen.getByLabelText('Channel'), '7')
  expect(
    await screen.findByRole('switch', { name: 'Enable asset library' })
  ).toHaveAttribute('aria-disabled', 'true')
  expect(screen.getByRole('alert')).toHaveTextContent(
    'Asset libraries require a single-key channel.'
  )
})

test('channel selection pages on the server and keeps the current channel selectable', async () => {
  const user = userEvent.setup()
  const thirdChannel = { ...channel, id: 9, name: 'Third channel' }
  channelConfigs[9] = {
    ...channelConfigs[8],
    channel_id: 9,
    endpoint: 'https://third.example.com/assets',
  }
  const get = vi.mocked(api.get).getMockImplementation()
  if (!get) throw new Error('Channel request fixture is missing')
  vi.mocked(api.get).mockImplementation(async (url, options) => {
    if (url === '/api/channel') {
      const page = (options?.params as { p?: number } | undefined)?.p ?? 1
      return {
        data: {
          success: true,
          data: {
            items: page === 1 ? [channel] : [otherChannel, thirdChannel],
            total: 22,
          },
        },
      }
    }
    return get(url, options)
  })
  render(
    <QueryClientProvider client={client}>
      <AssetChannelSettingsDialog onClose={vi.fn()} />
    </QueryClientProvider>
  )
  await screen.findByRole('option', { name: 'Video channel (#7)' })
  await user.selectOptions(screen.getByLabelText('Channel'), '7')
  await screen.findByLabelText('Asset API endpoint')
  await user.click(screen.getByRole('button', { name: 'Next page' }))
  await screen.findByRole('option', { name: 'Second channel (#8)' })
  expect(screen.getByLabelText('Channel')).toHaveValue('7')
  expect(
    screen.getByRole('option', { name: 'Video channel (#7)' })
  ).toBeInTheDocument()
  expect(api.get).toHaveBeenCalledWith(
    '/api/channel',
    expect.objectContaining({
      params: expect.objectContaining({ p: 2, page_size: 20, type: 45 }),
    })
  )
  await user.selectOptions(screen.getByLabelText('Channel'), '8')
  expect(
    await screen.findByDisplayValue('https://second.example.com/assets')
  ).toBeVisible()
  await waitFor(() =>
    expect(screen.getByRole('button', { name: 'Previous page' })).toBeEnabled()
  )
  expect(screen.getByRole('button', { name: 'Next page' })).toBeDisabled()
  expect(
    screen.queryByRole('option', { name: 'Video channel (#7)' })
  ).not.toBeInTheDocument()
  await user.selectOptions(screen.getByLabelText('Channel'), '9')
  await screen.findByDisplayValue('https://third.example.com/assets')
  await waitFor(() =>
    expect(screen.getByRole('button', { name: 'Previous page' })).toBeEnabled()
  )
  expect(screen.getByRole('button', { name: 'Next page' })).toBeDisabled()
  expect(
    screen.queryByRole('option', { name: 'Video channel (#7)' })
  ).not.toBeInTheDocument()
})

test('verification and saving lock channel selection and clear a replacement secret on success', async () => {
  const user = userEvent.setup()
  let resolveVerification: (
    value: Awaited<ReturnType<typeof api.post>>
  ) => void = () => {
    throw new Error('Verification is not pending')
  }
  let resolveSave: (
    value: Awaited<ReturnType<typeof api.put>>
  ) => void = () => {
    throw new Error('Save is not pending')
  }
  vi.mocked(api.post).mockImplementation(
    () =>
      new Promise((resolve) => {
        resolveVerification = resolve
      })
  )
  const put = vi.spyOn(api, 'put').mockImplementation(
    () =>
      new Promise((resolve) => {
        resolveSave = resolve
      })
  )
  render(
    <QueryClientProvider client={client}>
      <AssetChannelSettingsDialog onClose={vi.fn()} />
    </QueryClientProvider>
  )
  await screen.findByRole('option', { name: 'Second channel (#8)' })
  await user.selectOptions(screen.getByLabelText('Channel'), '8')
  await user.type(
    await screen.findByLabelText('Secret key'),
    'replacement-secret'
  )
  await user.click(screen.getByRole('button', { name: 'Save' }))
  await waitFor(() => expect(api.post).toHaveBeenCalledOnce())
  expect(screen.getByLabelText('Channel')).toBeDisabled()
  expect(put).not.toHaveBeenCalled()
  resolveVerification({
    data: {
      success: true,
      data: {
        proof_token: 'fresh-proof',
        scope: 'channel.assets.write',
        method: 'session',
        expires_at: Math.floor(Date.now() / 1000) + 60,
      },
    },
  })
  await waitFor(() => expect(put).toHaveBeenCalledOnce())
  expect(screen.getByLabelText('Channel')).toBeDisabled()
  expect(put).toHaveBeenCalledWith(
    '/api/channel/8/assets',
    expect.objectContaining({ secret_key: 'replacement-secret', revision: 3 }),
    expect.objectContaining({ headers: { 'X-Security-Proof': 'fresh-proof' } })
  )
  resolveSave({
    data: { success: true, data: { ...channelConfigs[8], revision: 4 } },
  })
  await waitFor(() => expect(screen.getByLabelText('Channel')).toBeEnabled())
  expect(screen.getByLabelText('Secret key')).toHaveValue('')
  expect(screen.getByRole('button', { name: 'Save' })).toBeDisabled()
})

test('a rejected verification never saves channel settings and cancelling retains the draft', async () => {
  const user = userEvent.setup()
  vi.mocked(api.post).mockResolvedValue({
    data: { success: false, message: 'Verification failed' },
  })
  const put = vi.spyOn(api, 'put')
  render(
    <QueryClientProvider client={client}>
      <AssetChannelSettingsDialog onClose={vi.fn()} />
    </QueryClientProvider>
  )
  await screen.findByRole('option', { name: 'Video channel (#7)' })
  await user.selectOptions(screen.getByLabelText('Channel'), '7')
  await user.type(
    await screen.findByLabelText('Upstream project name'),
    '-draft'
  )
  await user.click(screen.getByRole('button', { name: 'Save' }))
  await waitFor(() => expect(api.post).toHaveBeenCalledOnce())
  await user.click(await screen.findByRole('button', { name: 'Cancel' }))
  await waitFor(() =>
    expect(screen.getByRole('button', { name: 'Save' })).toBeEnabled()
  )
  expect(screen.getByLabelText('Upstream project name')).toHaveValue(
    'default-draft'
  )
  expect(put).not.toHaveBeenCalled()
})

test('a failed channel save retains the draft and a blank saved secret is never sent', async () => {
  const user = userEvent.setup()
  const put = vi.spyOn(api, 'put').mockResolvedValue({
    data: {
      success: false,
      message: 'Asset settings changed. Reload and try again.',
    },
  })
  render(
    <QueryClientProvider client={client}>
      <AssetChannelSettingsDialog onClose={vi.fn()} />
    </QueryClientProvider>
  )
  await screen.findByRole('option', { name: 'Second channel (#8)' })
  await user.selectOptions(screen.getByLabelText('Channel'), '8')
  await user.type(
    await screen.findByLabelText('Upstream project name'),
    '-draft'
  )
  await user.click(screen.getByRole('button', { name: 'Save' }))
  await waitFor(() => expect(put).toHaveBeenCalledOnce())
  await waitFor(() =>
    expect(screen.getByRole('button', { name: 'Save' })).toBeEnabled()
  )
  expect(screen.getByLabelText('Upstream project name')).toHaveValue(
    'second-draft'
  )
  expect(put).toHaveBeenCalledWith(
    '/api/channel/8/assets',
    {
      enabled: true,
      endpoint: 'https://second.example.com/assets',
      project_name: 'second-draft',
      auth_type: 'aksk',
      access_key: 'second-access',
      revision: 3,
    },
    expect.objectContaining({ singleUseAuthorization: true })
  )
})

test('closing a dirty channel requires confirmation and preserves input when cancelled', async () => {
  const user = userEvent.setup()
  const close = vi.fn()
  render(
    <QueryClientProvider client={client}>
      <AssetChannelSettingsDialog onClose={close} />
    </QueryClientProvider>
  )
  await screen.findByRole('option', { name: 'Video channel (#7)' })
  await user.selectOptions(screen.getByLabelText('Channel'), '7')
  await user.type(
    await screen.findByLabelText('Upstream project name'),
    '-draft'
  )
  await user.keyboard('{Escape}')
  await user.click(await screen.findByRole('button', { name: 'Cancel' }))
  expect(close).not.toHaveBeenCalled()
  expect(screen.getByLabelText('Upstream project name')).toHaveValue(
    'default-draft'
  )
  await user.keyboard('{Escape}')
  await user.click(
    await screen.findByRole('button', { name: 'Discard changes' })
  )
  expect(close).toHaveBeenCalledOnce()
})

test('storage errors preserve unsaved secrets and an invalid endpoint prevents saving', async () => {
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
  await user.type(endpoint, 'ftp://invalid.example.com')
  await user.click(screen.getByRole('button', { name: 'Save Changes' }))
  expect(
    await screen.findByText('Storage endpoint must be an HTTP or HTTPS origin')
  ).toBeVisible()
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

test('legacy storage keeps its addressing mode and exposes all settings', async () => {
  vi.mocked(api.get).mockResolvedValue({
    data: { success: true, data: { ...config, force_path_style: true } },
  })
  render(
    <QueryClientProvider client={client}>
      <StorageFixture />
    </QueryClientProvider>
  )
  expect(
    await screen.findByRole('combobox', { name: 'S3 provider' })
  ).toHaveTextContent('Other S3-compatible storage')
  expect(
    screen.getByRole('switch', { name: 'Use path-style addressing' })
  ).toBeChecked()
  expect(screen.queryByText('Advanced settings')).not.toBeInTheDocument()
  expect(screen.getByRole('button', { name: 'Save Changes' })).toBeDisabled()
})

test('provider selection applies defaults while allowing a manual override', async () => {
  const user = userEvent.setup()
  render(
    <QueryClientProvider client={client}>
      <StorageFixture />
    </QueryClientProvider>
  )
  const provider = await screen.findByRole('combobox', { name: 'S3 provider' })
  const addressing = screen.getByRole('switch', {
    name: 'Use path-style addressing',
  })
  for (const [name, checked] of [
    ['MinIO', true],
    ['Volcengine TOS', false],
    ['Ceph RGW', true],
    ['Alibaba Cloud OSS', false],
    ['Oracle Cloud OCI', true],
    ['Tencent Cloud COS', false],
    ['Amazon S3', false],
  ] as const) {
    await user.click(provider)
    await user.click(screen.getByRole('option', { name }))
    expect(addressing).toHaveAttribute('aria-checked', String(checked))
  }
  await user.click(addressing)
  await user.click(provider)
  await user.click(
    screen.getByRole('option', { name: 'Other S3-compatible storage' })
  )
  expect(addressing).toBeChecked()
  expect(screen.getByLabelText('Bucket')).toHaveValue(config.bucket)
})

test('OCI endpoint changes choose the corresponding addressing style', async () => {
  const user = userEvent.setup()
  vi.mocked(api.get).mockResolvedValue({
    data: {
      success: true,
      data: { ...config, provider: 'oci', force_path_style: true },
    },
  })
  render(
    <QueryClientProvider client={client}>
      <StorageFixture />
    </QueryClientProvider>
  )
  const endpoint = await screen.findByLabelText('S3 endpoint')
  const addressing = screen.getByRole('switch', {
    name: 'Use path-style addressing',
  })
  await user.clear(endpoint)
  await user.type(
    endpoint,
    'vhcompat.objectstorage.us-ashburn-1.oci.customer-oci.com'
  )
  await user.tab()
  expect(addressing).not.toBeChecked()
  expect(endpoint).toHaveValue(
    'https://vhcompat.objectstorage.us-ashburn-1.oci.customer-oci.com'
  )
  await user.click(addressing)
  await user.click(endpoint)
  await user.tab()
  expect(addressing).toBeChecked()
  await user.clear(endpoint)
  await user.type(
    endpoint,
    'namespace.compat.objectstorage.us-ashburn-1.oraclecloud.com'
  )
  await user.tab()
  expect(addressing).toBeChecked()
})

test.each([
  ['http://minio.internal:9000/', 'http://minio.internal:9000'],
  ['tos-s3-cn-beijing.volces.com', 'https://tos-s3-cn-beijing.volces.com'],
  ['minio.internal:9000', 'https://minio.internal:9000'],
])(
  'storage endpoint %s is normalized and tested without verification',
  async (input, expected) => {
    const user = userEvent.setup()
    render(
      <QueryClientProvider client={client}>
        <StorageFixture />
      </QueryClientProvider>
    )
    const endpoint = await screen.findByLabelText('S3 endpoint')
    await user.clear(endpoint)
    await user.type(endpoint, input)
    await user.click(screen.getByRole('button', { name: 'Test Connection' }))
    await waitFor(() =>
      expect(api.post).toHaveBeenCalledWith(
        '/api/option/asset_storage/test',
        expect.objectContaining({ endpoint: expected }),
        expect.not.objectContaining({ singleUseAuthorization: true })
      )
    )
    expect(endpoint).toHaveValue(expected)
    expect(api.post).toHaveBeenCalledOnce()
  }
)

test.each([
  'http://',
  'https://',
  'https://storage.example.com?',
  'https://storage.example.com/objects',
  'https://user:secret@storage.example.com',
])('invalid storage origin %s prevents any request', async (input) => {
  const user = userEvent.setup()
  render(
    <QueryClientProvider client={client}>
      <StorageFixture />
    </QueryClientProvider>
  )
  const endpoint = await screen.findByLabelText('S3 endpoint')
  await user.clear(endpoint)
  await user.type(endpoint, input)
  await user.click(screen.getByRole('button', { name: 'Test Connection' }))
  expect(
    await screen.findByText('Storage endpoint must be an HTTP or HTTPS origin')
  ).toBeVisible()
  expect(endpoint).toHaveAttribute('aria-invalid', 'true')
  expect(api.post).not.toHaveBeenCalled()
})

test('storage fields use a responsive address row and a shared credential row', async () => {
  render(
    <QueryClientProvider client={client}>
      <StorageFixture />
    </QueryClientProvider>
  )
  const endpoint = await screen.findByLabelText('S3 endpoint')
  const endpointField = endpoint.closest('[data-slot="form-item"]')
  const addressRow = endpointField?.parentElement
  expect(addressRow).toHaveClass('grid', 'lg:grid-cols-4')
  expect(endpointField).toHaveClass('lg:col-span-2')
  expect(addressRow).toContainElement(screen.getByLabelText('Region'))
  expect(addressRow).toContainElement(screen.getByLabelText('Bucket'))
  const accessField = screen
    .getByLabelText('Access key')
    .closest('[data-slot="form-item"]')
  const secretField = screen
    .getByLabelText('Secret key')
    .closest('[data-slot="form-item"]')
  expect(accessField?.parentElement).toBe(secretField?.parentElement)
  expect(accessField?.parentElement).toHaveClass('grid', 'lg:grid-cols-2')
})

test('a saved provider preserves its override and keyboard selection persists the new provider', async () => {
  const user = userEvent.setup()
  vi.mocked(api.get).mockResolvedValue({
    data: {
      success: true,
      data: { ...config, provider: 'minio', force_path_style: false },
    },
  })
  const put = vi.spyOn(api, 'put').mockResolvedValue({
    data: {
      success: true,
      data: { ...config, provider: 'oci', force_path_style: true, revision: 3 },
    },
  })
  render(
    <QueryClientProvider client={client}>
      <StorageFixture />
    </QueryClientProvider>
  )
  const provider = await screen.findByRole('combobox', { name: 'S3 provider' })
  expect(provider).toHaveTextContent('MinIO')
  expect(
    screen.getByRole('switch', { name: 'Use path-style addressing' })
  ).not.toBeChecked()
  await user.click(provider)
  await user.keyboard('{End}{Enter}')
  expect(provider).toHaveTextContent('Oracle Cloud OCI')
  await user.click(screen.getByRole('button', { name: 'Save Changes' }))
  await waitFor(() =>
    expect(put).toHaveBeenCalledWith(
      '/api/option/asset_storage',
      expect.objectContaining({ provider: 'oci', force_path_style: true }),
      expect.anything()
    )
  )
  await waitFor(() =>
    expect(screen.getByRole('button', { name: 'Save Changes' })).toBeDisabled()
  )
  expect(provider).toHaveTextContent('Oracle Cloud OCI')
})
