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
import {
  act,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { toast } from 'sonner'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'

import { channelSchema } from '@/features/channels/types'
import { SettingsPageProvider } from '@/features/system-settings/components/settings-page-context'
import { AssetStorageSection } from '@/features/system-settings/operations/asset-storage-section'
import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth-store'

import { assetQueryKeys } from '../api'
import { AssetChannelSettingsDialog } from '../components/asset-channel-settings-dialog'
import type {
  AssetChannelConfig,
  AssetChannelUpdate,
  AssetStorageConfig,
} from '../types'

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
  useAuthStore.getState().auth.setBundle({
    access_token: 'asset-secret-test-session',
    token_type: 'Bearer',
    access_expires_at: Math.floor(Date.now() / 1000) + 3600,
    user: { id: 1, username: 'root', role: 100 },
    session: {
      sid: 'asset-secret-session',
      current: true,
      login_method: 'password',
      ip: '127.0.0.1',
      user_agent: 'test',
      created_at: 0,
      last_active_at: 0,
      expires_at: Math.floor(Date.now() / 1000) + 3600,
    },
  })
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
            scope: (options?.params as { scope: string } | undefined)?.scope,
            methods: [{ method: 'password', available: true }],
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
  vi.spyOn(api, 'post').mockImplementation(async (url, body) => {
    if (url === '/api/verify') {
      return {
        data: {
          success: true,
          data: {
            proof_token: 'asset-secret-proof',
            method: 'password',
            scope: (body as { scope: string }).scope,
            expires_at: Math.floor(Date.now() / 1000) + 60,
          },
        },
      }
    }
    if (String(url).endsWith('/secret')) {
      return {
        data: {
          success: true,
          data: {
            secret_key:
              url === '/api/option/asset_storage/secret'
                ? 'saved-storage-secret'
                : 'saved-channel-secret',
          },
        },
      }
    }
    return { data: { success: true, data: {} } }
  })
})
afterEach(() => {
  client.clear()
  useAuthStore.getState().auth.reset()
})

async function renderSecretSettings(target: 'storage' | 'channel') {
  const user = userEvent.setup()
  const result = render(
    <QueryClientProvider client={client}>
      {target === 'storage' ? (
        <StorageFixture />
      ) : (
        <AssetChannelSettingsDialog onClose={vi.fn()} />
      )}
    </QueryClientProvider>
  )
  if (target === 'channel') {
    await screen.findByRole('button', { name: 'Edit Second channel (#8)' })
    await user.click(
      await screen.findByRole('button', { name: 'Edit Second channel (#8)' })
    )
  }
  await screen.findByLabelText('Secret key')
  return { user, ...result }
}

test.each(['storage', 'channel'] as const)(
  'root can verify and view a saved %s secret without saving or caching it',
  async (target) => {
    const put = vi.spyOn(api, 'put')
    const { user } = await renderSecretSettings(target)
    const secret = `saved-${target}-secret`
    await user.type(screen.getByLabelText('Secret key'), 'replacement-draft')
    await user.click(screen.getByRole('button', { name: 'Verify to view' }))
    await user.type(
      await screen.findByLabelText('Password', { selector: 'input' }),
      'reveal-password'
    )
    await user.click(screen.getByRole('button', { name: 'Verify' }))
    const dialog = await screen.findByRole('dialog', {
      name: 'Saved secret key',
    })
    expect(
      within(dialog).getByRole('textbox', { name: 'Saved secret key' })
    ).toHaveValue(secret)
    expect(
      within(dialog).getByRole('textbox', { name: 'Saved secret key' })
    ).toHaveAttribute('readonly')
    expect(screen.getByLabelText('Secret key')).toHaveValue('replacement-draft')
    expect(put).not.toHaveBeenCalled()
    expect(api.post).toHaveBeenCalledWith(
      '/api/verify',
      {
        method: 'password',
        password: 'reveal-password',
        scope:
          target === 'storage'
            ? 'asset.storage.secret.read'
            : 'channel.assets.secret.read',
        ...(target === 'channel' ? { context: { channel_id: 8 } } : {}),
      },
      expect.anything()
    )
    expect(api.post).toHaveBeenCalledWith(
      target === 'storage'
        ? '/api/option/asset_storage/secret'
        : '/api/channel/8/assets/secret',
      undefined,
      expect.objectContaining({
        singleUseAuthorization: true,
        headers: { 'X-Security-Proof': 'asset-secret-proof' },
        signal: expect.any(AbortSignal),
      })
    )
    expect(
      JSON.stringify(
        client
          .getQueryCache()
          .getAll()
          .map((query) => query.state.data)
      )
    ).not.toContain(secret)
    expect(client.getMutationCache().getAll()).toHaveLength(0)
    await user.click(
      within(dialog).getByRole('button', { name: 'Copy to clipboard' })
    )
    expect(await navigator.clipboard.readText()).toBe(secret)
    await user.click(within(dialog).getByRole('button', { name: 'Close' }))
    expect(put).not.toHaveBeenCalled()
    expect(
      screen.queryByRole('textbox', { name: 'Saved secret key' })
    ).not.toBeInTheDocument()
    expect(
      screen.queryByRole('button', { name: 'Copied' })
    ).not.toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Verify to view' }))
    expect(
      await screen.findByLabelText('Password', { selector: 'input' })
    ).toHaveValue('')
    expect(
      screen.queryByRole('textbox', { name: 'Saved secret key' })
    ).not.toBeInTheDocument()
  }
)

test('cancelling verification leaves the saved storage secret unread and the draft untouched', async () => {
  const { user } = await renderSecretSettings('storage')
  await user.type(screen.getByLabelText('Secret key'), 'keep-draft')
  const trigger = screen.getByRole('button', { name: 'Verify to view' })
  trigger.focus()
  await user.keyboard('{Enter}')
  await screen.findByLabelText('Password', { selector: 'input' })
  await user.keyboard('{Escape}')
  expect(api.post).not.toHaveBeenCalled()
  expect(screen.getByLabelText('Secret key')).toHaveValue('keep-draft')
  expect(trigger).toBeEnabled()
  await waitFor(() => expect(trigger).toHaveFocus())
})

test('failed password verification does not request the saved secret or submit settings', async () => {
  vi.mocked(api.post).mockResolvedValueOnce({
    data: {
      success: false,
      code: 'SECURITY_VERIFICATION_FAILED',
      message: 'Verification failed. Please try again.',
    },
  })
  const put = vi.spyOn(api, 'put')
  const { user } = await renderSecretSettings('storage')
  await user.click(screen.getByRole('button', { name: 'Verify to view' }))
  await user.type(
    await screen.findByLabelText('Password', { selector: 'input' }),
    'wrong-password'
  )
  await user.click(screen.getByRole('button', { name: 'Verify' }))
  expect(await screen.findByRole('alert')).toHaveTextContent(
    'Verification failed. Please try again.'
  )
  expect(api.post).toHaveBeenCalledTimes(1)
  expect(put).not.toHaveBeenCalled()
  expect(
    screen.queryByRole('textbox', { name: 'Saved secret key' })
  ).not.toBeInTheDocument()
})

test('a failed secret read reports the error and requires fresh verification', async () => {
  const errorToast = vi.spyOn(toast, 'error').mockImplementation(() => 1)
  const post = vi.mocked(api.post).getMockImplementation()
  if (!post) throw new Error('Secret request fixture is missing')
  vi.mocked(api.post).mockImplementation(async (url, ...args) => {
    if (String(url).endsWith('/secret')) {
      return { data: { success: false, message: 'No secret is saved.' } }
    }
    return post(url, ...args)
  })
  const { user } = await renderSecretSettings('storage')
  await user.click(screen.getByRole('button', { name: 'Verify to view' }))
  await user.type(
    await screen.findByLabelText('Password', { selector: 'input' }),
    'reveal-password'
  )
  await user.click(screen.getByRole('button', { name: 'Verify' }))
  await waitFor(() =>
    expect(errorToast).toHaveBeenCalledWith('No secret is saved.')
  )
  expect(
    screen.queryByRole('textbox', { name: 'Saved secret key' })
  ).not.toBeInTheDocument()
  await user.click(screen.getByRole('button', { name: 'Verify to view' }))
  expect(
    await screen.findByLabelText('Password', { selector: 'input' })
  ).toHaveValue('')
})

test.each(['close', 'unmount', 'revision', 'session', 'logout'] as const)(
  '%s cancels a pending saved-secret read and ignores its late response',
  async (change) => {
    let resolveRead!: (response: {
      data: { success: boolean; data: { secret_key: string } }
    }) => void
    const pending = new Promise<{
      data: { success: boolean; data: { secret_key: string } }
    }>((resolve) => {
      resolveRead = resolve
    })
    const post = vi.mocked(api.post).getMockImplementation()
    if (!post) throw new Error('Secret request fixture is missing')
    let signal: AbortSignal | undefined
    vi.mocked(api.post).mockImplementation(async (url, body, options) => {
      if (String(url).endsWith('/secret')) {
        signal = options?.signal as AbortSignal
        return pending
      }
      return post(url, body, options)
    })
    const { user, unmount } = await renderSecretSettings('storage')
    await user.click(screen.getByRole('button', { name: 'Verify to view' }))
    await user.type(
      await screen.findByLabelText('Password', { selector: 'input' }),
      'reveal-password'
    )
    await user.click(screen.getByRole('button', { name: 'Verify' }))
    const dialog = await screen.findByRole('dialog', {
      name: 'Saved secret key',
    })
    expect(within(dialog).getByText('Loading...')).toBeVisible()
    if (change === 'close') {
      await user.click(within(dialog).getByRole('button', { name: 'Close' }))
    } else if (change === 'unmount') {
      unmount()
    } else {
      act(() => {
        if (change === 'revision') {
          client.setQueryData(assetQueryKeys.storage, {
            ...config,
            revision: 3,
          })
        } else if (change === 'logout') useAuthStore.getState().auth.reset()
        else {
          const auth = useAuthStore.getState().auth
          if (!auth.session) throw new Error('Login session fixture is missing')
          useAuthStore.setState({
            auth: { ...auth, session: { ...auth.session, sid: 'new-session' } },
          })
        }
      })
    }
    await waitFor(() => expect(signal?.aborted).toBe(true))
    await act(async () => {
      resolveRead({
        data: { success: true, data: { secret_key: 'late-secret' } },
      })
    })
    expect(
      screen.queryByRole('textbox', { name: 'Saved secret key' })
    ).not.toBeInTheDocument()
    expect(screen.queryByText('late-secret')).not.toBeInTheDocument()
  }
)

test('changing channels after disclosure requires verification for the newly selected channel', async () => {
  channelConfigs[7] = {
    ...channelConfigs[7],
    auth_type: 'aksk',
    has_secret: true,
  }
  const { user } = await renderSecretSettings('channel')
  await user.click(screen.getByRole('button', { name: 'Verify to view' }))
  await user.type(
    await screen.findByLabelText('Password', { selector: 'input' }),
    'reveal-password'
  )
  await user.click(screen.getByRole('button', { name: 'Verify' }))
  const dialog = await screen.findByRole('dialog', { name: 'Saved secret key' })
  await user.click(within(dialog).getByRole('button', { name: 'Close' }))
  await user.click(screen.getByRole('button', { name: 'Back to list' }))
  await user.click(
    await screen.findByRole('button', { name: 'Edit Video channel (#7)' })
  )
  await screen.findByLabelText('Secret key')
  expect(
    screen.queryByRole('textbox', { name: 'Saved secret key' })
  ).not.toBeInTheDocument()
  await user.click(screen.getByRole('button', { name: 'Verify to view' }))
  await user.type(
    await screen.findByLabelText('Password', { selector: 'input' }),
    'reveal-password'
  )
  await user.click(screen.getByRole('button', { name: 'Verify' }))
  await screen.findByRole('textbox', { name: 'Saved secret key' })
  expect(api.post).toHaveBeenLastCalledWith(
    '/api/channel/7/assets/secret',
    undefined,
    expect.anything()
  )
  expect(api.post).toHaveBeenCalledWith(
    '/api/verify',
    expect.objectContaining({ context: { channel_id: 7 } }),
    expect.anything()
  )
})

test.each(['no saved secret', 'non-root'] as const)(
  '%s has no saved-secret disclosure action',
  async (condition) => {
    if (condition === 'non-root') {
      useAuthStore
        .getState()
        .auth.setUser({ id: 1, username: 'admin', role: 10 })
    } else {
      const get = vi.mocked(api.get).getMockImplementation()
      if (!get) throw new Error('Storage request fixture is missing')
      vi.mocked(api.get).mockImplementation(async (url, ...args) => {
        if (url === '/api/option/asset_storage') {
          return {
            data: { success: true, data: { ...config, has_secret: false } },
          }
        }
        return get(url, ...args)
      })
    }
    await renderSecretSettings('storage')
    expect(
      screen.queryByRole('button', { name: 'Verify to view' })
    ).not.toBeInTheDocument()
    expect(api.post).not.toHaveBeenCalled()
  }
)

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

test('editing a listed channel loads only its settings and saving returns to the list', async () => {
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
  const edit = await screen.findByRole('button', {
    name: 'Edit Video channel (#7)',
  })
  expect(screen.getByRole('table')).toBeVisible()
  expect(screen.queryByLabelText('Channel')).not.toBeInTheDocument()
  expect(api.get).not.toHaveBeenCalledWith(
    '/api/channel/7/assets',
    expect.anything()
  )
  await user.click(edit)
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
  expect(api.post).not.toHaveBeenCalled()
  expect(api.get).not.toHaveBeenCalledWith(
    '/api/verify/methods',
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
      size_limits_mb: { image: 0, video: 0, audio: 0 },
    },
    { skipBusinessError: true, skipErrorHandler: true }
  )
  expect(
    await screen.findByRole('button', { name: 'Edit Video channel (#7)' })
  ).toBeVisible()
  expect(screen.queryByLabelText('Asset API endpoint')).not.toBeInTheDocument()
})

test('returning from a dirty channel confirms discard and clears credentials before editing another', async () => {
  const user = userEvent.setup()
  client.setDefaultOptions({ queries: { retry: false, staleTime: 10_000 } })
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
  await user.click(
    await screen.findByRole('button', { name: 'Edit Video channel (#7)' })
  )
  await user.type(await screen.findByLabelText('Secret key'), 'first-secret')
  await user.click(screen.getByRole('button', { name: 'Back to list' }))
  expect(await screen.findByRole('alertdialog')).toHaveTextContent(
    'Unsaved changes'
  )
  await user.click(screen.getByRole('button', { name: 'Cancel' }))
  expect(
    screen.getByRole('dialog', { name: 'Channel settings' })
  ).toHaveAccessibleDescription('Video channel (#7)')
  expect(screen.getByLabelText('Secret key')).toHaveValue('first-secret')
  await user.click(screen.getByRole('button', { name: 'Back to list' }))
  await user.click(
    await screen.findByRole('button', { name: 'Discard changes' })
  )
  await waitFor(() =>
    expect(
      screen.getByRole('button', { name: 'Edit Video channel (#7)' })
    ).toHaveFocus()
  )
  await user.click(
    await screen.findByRole('button', { name: 'Edit Second channel (#8)' })
  )
  expect(
    await screen.findByDisplayValue('https://second.example.com/assets')
  ).toBeVisible()
  expect(screen.getByLabelText('Secret key')).toHaveValue('')
  expect(screen.getByLabelText('Access key')).toHaveValue('second-access')
  expect(screen.getByRole('button', { name: 'Save' })).toBeDisabled()
})

test('channel size overrides save together and clearing restores official defaults', async () => {
  const user = userEvent.setup()
  const put = vi.spyOn(api, 'put').mockImplementation(async (_url, body) => {
    channelConfigs[7] = {
      ...channelConfigs[7],
      ...(body as AssetChannelUpdate),
      revision: channelConfigs[7].revision + 1,
    }
    return { data: { success: true, data: channelConfigs[7] } }
  })
  client.setQueryData(['assets', 'config'], {
    enabled: true,
    size_limits_mb: { image: 30, video: 200, audio: 15 },
  })
  render(
    <QueryClientProvider client={client}>
      <AssetChannelSettingsDialog onClose={vi.fn()} />
    </QueryClientProvider>
  )
  await user.click(
    await screen.findByRole('button', { name: 'Edit Video channel (#7)' })
  )
  const image = await screen.findByLabelText('Image limit (MB)')
  const video = screen.getByLabelText('Video limit (MB)')
  const audio = screen.getByLabelText('Audio limit (MB)')
  expect(image).toHaveValue(null)
  expect(image).toHaveAttribute('placeholder', '30')
  expect(video).toHaveAttribute('placeholder', '200')
  expect(audio).toHaveAttribute('placeholder', '15')
  await user.type(image, '20')
  await user.type(video, '50')
  await user.type(audio, '10')
  await user.click(screen.getByRole('button', { name: 'Save' }))
  await waitFor(() => expect(put).toHaveBeenCalledOnce())
  expect(put.mock.calls[0][1]).toEqual(
    expect.objectContaining({
      size_limits_mb: { image: 20, video: 50, audio: 10 },
    })
  )
  expect(client.getQueryState(['assets', 'config'])?.isInvalidated).toBe(true)
  await user.click(
    await screen.findByRole('button', { name: 'Edit Video channel (#7)' })
  )
  await user.clear(await screen.findByLabelText('Video limit (MB)'))
  await user.click(screen.getByRole('button', { name: 'Save' }))
  await waitFor(() => expect(put).toHaveBeenCalledTimes(2))
  expect(put.mock.calls[1][1]).toEqual(
    expect.objectContaining({
      size_limits_mb: { image: 20, video: 0, audio: 10 },
    })
  )
})

test('invalid channel size limit associates its validation error with the input', async () => {
  const user = userEvent.setup()
  const put = vi.spyOn(api, 'put')
  render(
    <QueryClientProvider client={client}>
      <AssetChannelSettingsDialog onClose={vi.fn()} />
    </QueryClientProvider>
  )
  await user.click(
    await screen.findByRole('button', { name: 'Edit Video channel (#7)' })
  )
  const image = await screen.findByLabelText('Image limit (MB)')
  await user.type(image, '-1')
  const form = image.closest('form')
  if (!form) throw new Error('Channel form not found')
  fireEvent.submit(form)
  expect(
    await screen.findByText('Enter a non-negative whole number')
  ).toBeVisible()
  expect(image).toHaveAttribute('aria-invalid', 'true')
  expect(image).toHaveAccessibleDescription(/Enter a non-negative whole number/)
  expect(put).not.toHaveBeenCalled()
})

test('channel settings can search by name or ID without fetching each result configuration', async () => {
  const user = userEvent.setup()
  render(
    <QueryClientProvider client={client}>
      <AssetChannelSettingsDialog onClose={vi.fn()} />
    </QueryClientProvider>
  )
  await screen.findByRole('button', { name: 'Edit Video channel (#7)' })
  await user.type(
    screen.getByRole('searchbox', { name: 'Search channels' }),
    '8'
  )
  await user.click(screen.getByRole('button', { name: 'Search' }))
  await waitFor(() =>
    expect(
      screen.queryByRole('button', { name: 'Edit Video channel (#7)' })
    ).not.toBeInTheDocument()
  )
  expect(
    screen.getByRole('button', { name: 'Edit Second channel (#8)' })
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

test('returning to search results preserves the draft and applied keyword', async () => {
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
  await screen.findByRole('button', { name: 'Edit Unrelated (#9)' })
  await user.type(
    screen.getByRole('searchbox', { name: 'Search channels' }),
    'channel'
  )
  await user.click(screen.getByRole('button', { name: 'Search' }))
  await waitFor(() =>
    expect(
      screen.queryByRole('button', { name: 'Edit Unrelated (#9)' })
    ).not.toBeInTheDocument()
  )
  await user.type(
    screen.getByRole('searchbox', { name: 'Search channels' }),
    '-draft'
  )
  await user.click(
    await screen.findByRole('button', { name: 'Edit Video channel (#7)' })
  )
  await user.type(await screen.findByLabelText('Secret key'), 'first-secret')
  await user.click(screen.getByRole('button', { name: 'Back to list' }))
  await user.click(
    await screen.findByRole('button', { name: 'Discard changes' })
  )
  expect(
    await screen.findByRole('searchbox', { name: 'Search channels' })
  ).toHaveValue('channel-draft')
  expect(
    screen.queryByRole('button', { name: 'Edit Unrelated (#9)' })
  ).not.toBeInTheDocument()
  expect(
    screen.getByRole('button', { name: 'Edit Video channel (#7)' })
  ).toBeInTheDocument()
  await user.click(
    await screen.findByRole('button', { name: 'Edit Second channel (#8)' })
  )
  await screen.findByDisplayValue('https://second.example.com/assets')
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
  await screen.findByRole('button', { name: 'Edit Video channel (#7)' })
  expect(
    screen.queryByRole('button', { name: /Edit OpenAI/ })
  ).not.toBeInTheDocument()
  expect(
    screen.queryByRole('button', { name: /Edit Doubao/ })
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
    screen.getByRole('button', { name: 'Edit Video channel (#7)' })
  ).toBeVisible()
  expect(
    screen.queryByRole('button', { name: /Edit OpenAI/ })
  ).not.toBeInTheDocument()
  expect(
    screen.queryByRole('button', { name: /Edit Doubao/ })
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
  expect(
    screen.queryByRole('button', { name: /^Edit / })
  ).not.toBeInTheDocument()
  expect(screen.queryByRole('button', { name: 'Save' })).not.toBeInTheDocument()
})

test('failed channel details can be retried or left without losing the list', async () => {
  const user = userEvent.setup()
  const get = vi.mocked(api.get).getMockImplementation()
  if (!get) throw new Error('Channel request fixture is missing')
  vi.mocked(api.get).mockImplementation(async (url, options) => {
    if (url === '/api/channel/7/assets') throw new Error('Settings unavailable')
    return get(url, options)
  })
  render(
    <QueryClientProvider client={client}>
      <AssetChannelSettingsDialog onClose={vi.fn()} />
    </QueryClientProvider>
  )
  await user.click(
    await screen.findByRole('button', { name: 'Edit Video channel (#7)' })
  )
  expect(await screen.findByText('Settings unavailable')).toBeVisible()
  expect(screen.getByRole('dialog')).toHaveAccessibleDescription(
    'Video channel (#7)'
  )
  vi.mocked(api.get).mockImplementation(get)
  await user.click(screen.getByRole('button', { name: 'Retry' }))
  await screen.findByLabelText('Asset API endpoint')
  await user.click(screen.getByRole('button', { name: 'Back to list' }))
  expect(
    await screen.findByRole('button', { name: 'Edit Video channel (#7)' })
  ).toBeVisible()
})

test('leaving pending channel details prevents a late response from replacing another editor', async () => {
  const user = userEvent.setup()
  const get = vi.mocked(api.get).getMockImplementation()
  if (!get) throw new Error('Channel request fixture is missing')
  let resolveDetails!: (value: Awaited<ReturnType<typeof api.get>>) => void
  vi.mocked(api.get).mockImplementation(async (url, options) => {
    if (url === '/api/channel/7/assets') {
      return new Promise((resolve) => {
        resolveDetails = resolve
      })
    }
    return get(url, options)
  })
  render(
    <QueryClientProvider client={client}>
      <AssetChannelSettingsDialog onClose={vi.fn()} />
    </QueryClientProvider>
  )
  await user.click(
    await screen.findByRole('button', { name: 'Edit Video channel (#7)' })
  )
  expect(await screen.findByText('Loading...')).toBeVisible()
  await user.click(screen.getByRole('button', { name: 'Back to list' }))
  await user.click(
    await screen.findByRole('button', { name: 'Edit Second channel (#8)' })
  )
  await screen.findByDisplayValue('https://second.example.com/assets')
  await act(async () =>
    resolveDetails({ data: { success: true, data: channelConfigs[7] } })
  )
  expect(screen.getByRole('dialog')).toHaveAccessibleDescription(
    'Second channel (#8)'
  )
  expect(screen.getByLabelText('Asset API endpoint')).toHaveValue(
    'https://second.example.com/assets'
  )
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
  await user.click(
    await screen.findByRole('button', { name: 'Edit Video channel (#7)' })
  )
  expect(
    await screen.findByRole('switch', { name: 'Enable asset library' })
  ).toHaveAttribute('aria-disabled', 'true')
  expect(screen.getByRole('alert')).toHaveTextContent(
    'Asset libraries require a single-key channel.'
  )
})

test('returning from keyboard editing restores the server page, scroll position and edit focus', async () => {
  const user = userEvent.setup()
  client.setDefaultOptions({ queries: { retry: false, staleTime: 10_000 } })
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
  await screen.findByRole('button', { name: 'Edit Video channel (#7)' })
  await user.click(screen.getByRole('button', { name: 'Next page' }))
  const edit = await screen.findByRole('button', {
    name: 'Edit Second channel (#8)',
  })
  expect(api.get).toHaveBeenCalledWith(
    '/api/channel',
    expect.objectContaining({
      params: expect.objectContaining({ p: 2, page_size: 20, type: 45 }),
    })
  )
  fireEvent.scroll(screen.getByRole('region', { name: 'Channels' }), {
    target: { scrollTop: 120 },
  })
  edit.focus()
  await user.keyboard('{Enter}')
  expect(
    await screen.findByDisplayValue('https://second.example.com/assets')
  ).toBeVisible()
  await user.click(screen.getByRole('button', { name: 'Back to list' }))
  await waitFor(() =>
    expect(screen.getByRole('button', { name: 'Previous page' })).toBeEnabled()
  )
  expect(screen.getByRole('button', { name: 'Next page' })).toBeDisabled()
  expect(
    screen.queryByRole('button', { name: 'Edit Video channel (#7)' })
  ).not.toBeInTheDocument()
  expect(screen.getByRole('region', { name: 'Channels' }).scrollTop).toBe(120)
  await waitFor(() =>
    expect(
      screen.getByRole('button', { name: 'Edit Second channel (#8)' })
    ).toHaveFocus()
  )
  await user.click(
    await screen.findByRole('button', { name: 'Edit Third channel (#9)' })
  )
  await screen.findByDisplayValue('https://third.example.com/assets')
  await user.click(screen.getByRole('button', { name: 'Back to list' }))
  await waitFor(() =>
    expect(screen.getByRole('button', { name: 'Previous page' })).toBeEnabled()
  )
  expect(screen.getByRole('button', { name: 'Next page' })).toBeDisabled()
  expect(
    screen.queryByRole('button', { name: 'Edit Video channel (#7)' })
  ).not.toBeInTheDocument()
})

test('saving prevents leaving the editor and returns to the list with credentials cleared', async () => {
  const user = userEvent.setup()
  let resolveSave: (
    value: Awaited<ReturnType<typeof api.put>>
  ) => void = () => {
    throw new Error('Save is not pending')
  }
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
  await user.click(
    await screen.findByRole('button', { name: 'Edit Second channel (#8)' })
  )
  await user.type(
    await screen.findByLabelText('Secret key'),
    'replacement-secret'
  )
  await user.click(screen.getByRole('button', { name: 'Save' }))
  await waitFor(() => expect(put).toHaveBeenCalledOnce())
  expect(screen.getByRole('button', { name: 'Back to list' })).toBeDisabled()
  expect(screen.getByRole('button', { name: 'Saving...' })).toBeDisabled()
  expect(screen.getByLabelText('Upstream project name')).toBeDisabled()
  await user.click(screen.getByRole('button', { name: 'Back to list' }))
  await user.keyboard('{Escape}')
  expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
  expect(screen.getByLabelText('Secret key')).toHaveValue('replacement-secret')
  expect(api.post).not.toHaveBeenCalled()
  expect(put).toHaveBeenCalledWith(
    '/api/channel/8/assets',
    expect.objectContaining({ secret_key: 'replacement-secret', revision: 3 }),
    { skipBusinessError: true, skipErrorHandler: true }
  )
  resolveSave({
    data: { success: true, data: { ...channelConfigs[8], revision: 4 } },
  })
  await user.click(
    await screen.findByRole('button', { name: 'Edit Second channel (#8)' })
  )
  await screen.findByLabelText('Secret key')
  expect(screen.getByLabelText('Secret key')).toHaveValue('')
  expect(screen.getByRole('button', { name: 'Save' })).toBeDisabled()
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
  await user.click(
    await screen.findByRole('button', { name: 'Edit Second channel (#8)' })
  )
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
      size_limits_mb: { image: 0, video: 0, audio: 0 },
    },
    { skipBusinessError: true, skipErrorHandler: true }
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
  await user.click(
    await screen.findByRole('button', { name: 'Edit Video channel (#7)' })
  )
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
