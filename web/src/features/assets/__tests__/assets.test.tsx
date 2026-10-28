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
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  RouterProvider,
} from '@tanstack/react-router'
import { act, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { toast } from 'sonner'
import { beforeEach, afterEach, expect, test, vi } from 'vitest'

import { api } from '@/lib/api'
import { ROLE } from '@/lib/roles'
import { useAuthStore } from '@/stores/auth-store'

import { AssetBrowser } from '../components/asset-browser'
import { AssetCreateDialog } from '../components/asset-create-dialog'
import { AssetGroupDialog } from '../components/asset-group-dialog'
import { AssetReplicasDialog } from '../components/asset-replicas-dialog'
import { AssetValidationDialog } from '../components/asset-validation-dialog'
import { Assets } from '../index'
import { assetValidationH5Link } from '../lib/validation'
import type { Asset, AssetGroup } from '../types'

const group: AssetGroup = {
  Id: 'group-one',
  Name: 'Avatars',
  Description: '',
  ProjectName: 'default',
  GroupType: 'AIGC',
  UserId: 23,
}
const replicaAsset: Asset = {
  Id: 'asset-na-avatar',
  Name: 'Avatar',
  GroupId: group.Id,
  ProjectName: 'default',
  AssetType: 'Image',
  Status: 'Active',
  URL: 'asset://asset-na-avatar',
  UserId: 23,
}
const failedReplica = {
  channel_id: 7,
  channel_name: 'Video channel',
  status: 'Failed',
  error_code: 'UpstreamInvalidResponse',
  error_message: 'The upstream material response was not valid JSON',
  last_attempt_at: 0,
  can_retry: true,
}
let client: QueryClient

async function renderAssetsPage() {
  const root = createRootRoute()
  const route = createRoute({
    getParentRoute: () => root,
    path: '/asset-library',
    component: Assets,
  })
  const storage = createRoute({
    getParentRoute: () => root,
    path: '/system-settings/operations/$section',
  })
  const router = createRouter({
    routeTree: root.addChildren([route, storage]),
    history: createMemoryHistory({ initialEntries: ['/asset-library'] }),
  })
  await router.load()
  render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  )
}

beforeEach(() => {
  client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  vi.spyOn(api, 'get').mockResolvedValue({
    data: {
      success: true,
      data: {
        enabled: true,
        size_limits_mb: { image: 30, video: 200, audio: 15 },
      },
    },
  })
})
afterEach(() => {
  client.clear()
  useAuthStore.getState().auth.reset()
})

test('replica resync preserves the asset and owner, prevents duplicate requests, and refreshes its status', async () => {
  const user = userEvent.setup()
  useAuthStore.getState().auth.setUser({
    id: 1,
    username: 'admin',
    role: ROLE.ADMIN,
    permissions: {
      admin_permissions: { channel: { read: true, write: true } },
    },
  })
  let queued = false
  const get = vi.spyOn(api, 'get').mockImplementation(async () => ({
    data: {
      success: true,
      data: [
        queued
          ? {
              ...failedReplica,
              status: 'Processing',
              error_code: '',
              error_message: '',
              can_retry: false,
            }
          : failedReplica,
        {
          ...failedReplica,
          channel_id: 6,
          channel_name: 'Ready channel',
          status: 'Active',
          error_code: '',
          error_message: '',
          can_retry: false,
        },
      ],
    },
  }))
  let complete!: (value: { data: { success: boolean; data: null } }) => void
  const post = vi.spyOn(api, 'post').mockImplementation(
    () =>
      new Promise((resolve) => {
        complete = resolve
      })
  )
  render(
    <QueryClientProvider client={client}>
      <AssetReplicasDialog
        asset={replicaAsset}
        scope={{ userId: 23 }}
        onClose={vi.fn()}
      />
    </QueryClientProvider>
  )
  const retry = await screen.findByRole('button', {
    name: 'Resync Video channel',
  })
  expect(screen.getAllByRole('columnheader')).toHaveLength(3)
  expect(
    within(screen.getByRole('cell', { name: /^Failed/ })).getByRole('button', {
      name: 'Resync Video channel',
    })
  ).toBe(retry)
  expect(
    screen.queryByRole('button', { name: 'Resync Ready channel' })
  ).not.toBeInTheDocument()
  await user.click(retry)
  expect(retry).toBeDisabled()
  await user.click(retry)
  expect(post).toHaveBeenCalledOnce()
  expect(post).toHaveBeenCalledWith(
    '/api/assets/asset-na-avatar/replicas/7/retry',
    undefined,
    expect.objectContaining({ params: { user_id: 23 } })
  )
  await act(async () => {
    queued = true
    complete({ data: { success: true, data: null } })
  })
  expect(await screen.findByText('Processing')).toBeVisible()
  expect(screen.getByText('Ready')).toBeVisible()
  expect(
    screen.queryByRole('button', { name: 'Resync Video channel' })
  ).not.toBeInTheDocument()
  expect(get).toHaveBeenCalledWith(
    '/api/assets/asset-na-avatar/replicas',
    expect.objectContaining({ params: { user_id: 23 } })
  )
})

test('rejected replica resync keeps the stored error visible and allows another attempt', async () => {
  const user = userEvent.setup()
  useAuthStore
    .getState()
    .auth.setUser({ id: 1, username: 'root', role: ROLE.SUPER_ADMIN })
  vi.spyOn(api, 'get').mockResolvedValue({
    data: { success: true, data: [failedReplica] },
  })
  const post = vi.spyOn(api, 'post').mockResolvedValue({
    data: { success: false, message: 'Upstream project is unavailable' },
  })
  const error = vi.spyOn(toast, 'error')
  render(
    <QueryClientProvider client={client}>
      <AssetReplicasDialog
        asset={replicaAsset}
        scope={{ userId: 23 }}
        onClose={vi.fn()}
      />
    </QueryClientProvider>
  )
  const retry = await screen.findByRole('button', {
    name: 'Resync Video channel',
  })
  await user.click(retry)
  await waitFor(() =>
    expect(error).toHaveBeenCalledWith('Upstream project is unavailable')
  )
  expect(retry).toBeEnabled()
  expect(
    screen.getByText(
      'UpstreamInvalidResponse: The upstream material response was not valid JSON'
    )
  ).toBeVisible()
  expect(screen.getByText('Failed')).toBeVisible()
  await user.click(retry)
  await waitFor(() => expect(post).toHaveBeenCalledTimes(2))
})

test('a size-rejected replica shows the file size and current channel limit', async () => {
  vi.mocked(api.get).mockResolvedValue({
    data: {
      success: true,
      data: [
        {
          ...failedReplica,
          error_code: 'FileSizeTooLarge',
          error_message: "Material exceeds this channel's size limit",
          file_size_bytes: 50000001,
          current_limit_bytes: 50000000,
        },
      ],
    },
  })
  render(
    <QueryClientProvider client={client}>
      <AssetReplicasDialog
        asset={replicaAsset}
        scope={{ userId: 23 }}
        onClose={vi.fn()}
      />
    </QueryClientProvider>
  )
  expect(
    await screen.findByText(
      'File: 50,000,001 bytes; current channel limit: 50,000,000 bytes'
    )
  ).toBeVisible()
  expect(
    screen.getByText(
      "FileSizeTooLarge: Material exceeds this channel's size limit"
    )
  ).toBeVisible()
})

test('a result arriving after close cannot replace or close a reopened verification', async () => {
  const user = userEvent.setup()
  vi.mocked(api.get).mockResolvedValue({
    data: {
      success: true,
      data: [
        { SessionId: 'validation-na-one', Name: 'Alice', Status: 'pending' },
      ],
    },
  })
  let finish!: (response: unknown) => void
  vi.spyOn(api, 'post').mockImplementation(async (_url, _body, options) => {
    const action = (options?.params as { Action?: string })?.Action
    if (action === 'GetVisualValidateResult') {
      return new Promise((resolve) => {
        finish = resolve
      })
    }
    if (action === 'CreateVisualValidateSession') {
      return {
        data: {
          success: true,
          data: {
            BytedToken: 'validation-na-two',
            Name: 'Bob',
            Status: 'pending',
            ExpiresAt: Date.now() / 1000 + 1800,
            H5Link: 'https://verify.example/bob',
          },
        },
      }
    }
    return {
      data: {
        success: true,
        data: {
          Items:
            action === 'ListAssetGroups'
              ? [{ ...group, GroupType: 'LivenessFace' }]
              : [],
          TotalCount: 0,
        },
      },
    }
  })
  render(
    <QueryClientProvider client={client}>
      <AssetBrowser project='default' scope={{}} admin={false} />
    </QueryClientProvider>
  )
  await screen.findByRole('option', { name: 'Avatars' })
  await user.selectOptions(
    screen.getByLabelText('Library type'),
    'LivenessFace'
  )
  expect(screen.getByLabelText('Library type')).toHaveValue('LivenessFace')
  await user.click(screen.getByRole('button', { name: 'Group actions' }))
  await user.click(screen.getByRole('menuitem', { name: 'Verify a person' }))
  await user.click(
    await screen.findByRole('button', {
      name: 'Continue verification for Alice',
    })
  )
  await user.click(
    within(screen.getByRole('dialog')).getAllByRole('button', {
      name: 'Close',
    })[0]
  )
  await waitFor(() =>
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  )
  await user.click(screen.getByRole('button', { name: 'Group actions' }))
  await user.click(screen.getByRole('menuitem', { name: 'Verify a person' }))
  await user.type(await screen.findByLabelText('Name'), 'Bob')
  await user.click(screen.getByRole('button', { name: 'Start verification' }))
  expect(
    await screen.findByRole('button', { name: 'Open verification' })
  ).toHaveAttribute('href', 'https://verify.example/bob?lng=en')
  await act(async () =>
    finish({
      data: {
        success: true,
        data: {
          BytedToken: 'validation-na-one',
          Name: 'Alice',
          Status: 'created',
          GroupId: 'group-alice',
        },
      },
    })
  )
  expect(screen.getByRole('dialog')).toHaveTextContent('Bob')
  expect(
    screen.getByRole('button', { name: 'Open verification' })
  ).toHaveAttribute('href', 'https://verify.example/bob?lng=en')
  expect(screen.getByLabelText('Asset group')).toHaveValue(group.Id)
})

test.each([
  { role: ROLE.ADMIN, write: false },
  { role: ROLE.USER, write: true },
])(
  'replica resync is hidden without administrative channel write access (%j)',
  async ({ role, write }) => {
    useAuthStore.getState().auth.setUser({
      id: 1,
      username: 'viewer',
      role,
      permissions: { admin_permissions: { channel: { read: true, write } } },
    })
    vi.spyOn(api, 'get').mockResolvedValue({
      data: { success: true, data: [failedReplica] },
    })
    const post = vi.spyOn(api, 'post')
    render(
      <QueryClientProvider client={client}>
        <AssetReplicasDialog
          asset={replicaAsset}
          scope={{ userId: 23 }}
          onClose={vi.fn()}
        />
      </QueryClientProvider>
    )
    expect(await screen.findByText('Failed')).toBeVisible()
    expect(
      screen.queryByRole('button', { name: /Resync/ })
    ).not.toBeInTheDocument()
    expect(post).not.toHaveBeenCalled()
  }
)

test('URL import preserves form input after a rejected response and retries the official action', async () => {
  const user = userEvent.setup()
  const close = vi.fn()
  const post = vi
    .spyOn(api, 'post')
    .mockResolvedValueOnce({
      data: { success: false, message: 'Asset source is unavailable' },
    })
    .mockResolvedValueOnce({
      data: { success: true, data: { Id: 'asset-one' } },
    })
  render(
    <QueryClientProvider client={client}>
      <AssetCreateDialog group={group} scope={{ userId: 23 }} onClose={close} />
    </QueryClientProvider>
  )
  await user.type(screen.getByLabelText('Name'), 'Dancing avatar')
  await user.type(
    screen.getByLabelText('Asset URL'),
    'https://example.com/avatar.png'
  )
  await user.click(screen.getByRole('button', { name: 'Add asset' }))
  await waitFor(() => expect(post).toHaveBeenCalledTimes(1))
  expect(close).not.toHaveBeenCalled()
  expect(screen.getByLabelText('Name')).toHaveValue('Dancing avatar')
  expect(screen.getByLabelText('Asset URL')).toHaveValue(
    'https://example.com/avatar.png'
  )
  await user.click(screen.getByRole('button', { name: 'Add asset' }))
  await waitFor(() => expect(close).toHaveBeenCalledOnce())
  expect(post).toHaveBeenLastCalledWith(
    '/api/assets/actions',
    {
      GroupId: 'group-one',
      ProjectName: 'default',
      AssetType: 'Image',
      Name: 'Dancing avatar',
      URL: 'https://example.com/avatar.png',
    },
    expect.objectContaining({
      params: {
        Action: 'CreateAsset',
        Version: '2024-01-01',
        user_id: 23,
        all_users: undefined,
      },
    })
  )
})

test('file upload requires a file and sends multipart with the selected media type', async () => {
  const user = userEvent.setup()
  const post = vi
    .spyOn(api, 'post')
    .mockResolvedValue({ data: { success: true, data: { Id: 'asset-one' } } })
  render(
    <QueryClientProvider client={client}>
      <AssetCreateDialog group={group} scope={{}} onClose={vi.fn()} />
    </QueryClientProvider>
  )
  await user.type(screen.getByLabelText('Name'), 'Voice')
  await user.selectOptions(screen.getByLabelText('Asset type'), 'Audio')
  await user.selectOptions(screen.getByLabelText('Source'), 'file')
  await user.click(screen.getByRole('button', { name: 'Add asset' }))
  expect(await screen.findByRole('alert')).toHaveTextContent('Select a file')
  expect(post).not.toHaveBeenCalled()
  const file = new File(['sound'], 'voice.wav', { type: 'audio/wav' })
  await user.upload(screen.getByLabelText('File'), file)
  await user.click(screen.getByRole('button', { name: 'Add asset' }))
  await waitFor(() => expect(post).toHaveBeenCalledOnce())
  const [url, body] = post.mock.calls[0]
  expect(url).toBe('/api/assets/upload')
  expect(body).toBeInstanceOf(FormData)
  expect((body as FormData).get('file')).toBe(file)
  expect((body as FormData).get('AssetType')).toBe('Audio')
  expect((body as FormData).get('GroupId')).toBe('group-one')
})

test.each([
  { type: 'Image', mime: 'image/png', size: 999999, allowed: true },
  { type: 'Image', mime: 'image/png', size: 1000000, allowed: false },
  { type: 'Video', mime: 'video/mp4', size: 1000000, allowed: true },
  { type: 'Video', mime: 'video/mp4', size: 1000001, allowed: false },
  { type: 'Audio', mime: 'audio/wav', size: 1000000, allowed: true },
  { type: 'Audio', mime: 'audio/wav', size: 1000001, allowed: false },
  { type: 'Audio', mime: 'audio/wav', size: 0, allowed: false },
])(
  'file admission uses the server limit ($type, $size bytes)',
  async ({ type, mime, size, allowed }) => {
    const user = userEvent.setup()
    vi.mocked(api.get).mockResolvedValue({
      data: {
        success: true,
        data: {
          enabled: true,
          size_limits_mb: { image: 1, video: 1, audio: 1 },
        },
      },
    })
    const post = vi.spyOn(api, 'post').mockResolvedValue({
      data: { success: true, data: { Id: 'asset-one' } },
    })
    render(
      <QueryClientProvider client={client}>
        <AssetCreateDialog group={group} scope={{}} onClose={vi.fn()} />
      </QueryClientProvider>
    )
    await user.type(screen.getByLabelText('Name'), 'Media')
    await user.selectOptions(screen.getByLabelText('Asset type'), type)
    const hint =
      type === 'Image'
        ? 'File size must be less than 1 MB.'
        : 'File size must not exceed 1 MB.'
    expect(await screen.findByText(hint, { exact: false })).toBeVisible()
    await user.selectOptions(screen.getByLabelText('Source'), 'file')
    expect(screen.getByText(hint, { exact: false })).toBeVisible()
    await user.upload(
      screen.getByLabelText('File'),
      new File([new Uint8Array(size)], 'media', { type: mime })
    )
    await user.click(screen.getByRole('button', { name: 'Add asset' }))
    if (allowed) {
      await waitFor(() => expect(post).toHaveBeenCalledOnce())
      expect(post.mock.calls[0][0]).toBe('/api/assets/upload')
    } else {
      expect(await screen.findByRole('alert')).toHaveTextContent(
        size === 0 ? 'The file is empty.' : hint
      )
      expect(screen.getByLabelText('File')).toHaveAttribute(
        'aria-invalid',
        'true'
      )
      expect(post).not.toHaveBeenCalled()
    }
  }
)

test('switching away from file upload clears the previously selected file', async () => {
  const user = userEvent.setup()
  const post = vi
    .spyOn(api, 'post')
    .mockResolvedValue({ data: { success: true, data: { Id: 'asset-one' } } })
  render(
    <QueryClientProvider client={client}>
      <AssetCreateDialog group={group} scope={{}} onClose={vi.fn()} />
    </QueryClientProvider>
  )
  await user.type(screen.getByLabelText('Name'), 'Product')
  await user.selectOptions(screen.getByLabelText('Source'), 'file')
  await user.upload(
    screen.getByLabelText('File'),
    new File(['image'], 'product.png', { type: 'image/png' })
  )
  await user.selectOptions(screen.getByLabelText('Source'), 'url')
  await user.selectOptions(screen.getByLabelText('Source'), 'file')
  await user.click(screen.getByRole('button', { name: 'Add asset' }))
  expect(await screen.findByRole('alert')).toHaveTextContent('Select a file')
  expect(post).not.toHaveBeenCalled()
})

test('failed limit loading blocks submission and retry preserves the asset draft', async () => {
  const user = userEvent.setup()
  const post = vi.spyOn(api, 'post')
  vi.mocked(api.get).mockRejectedValueOnce(new Error('Limits unavailable'))
  render(
    <QueryClientProvider client={client}>
      <AssetCreateDialog group={group} scope={{}} onClose={vi.fn()} />
    </QueryClientProvider>
  )
  await user.type(screen.getByLabelText('Name'), 'Media')
  expect(await screen.findByText('Limits unavailable')).toBeVisible()
  expect(screen.getByRole('button', { name: 'Add asset' })).toBeDisabled()
  await user.click(screen.getByRole('button', { name: 'Retry' }))
  expect(
    await screen.findByText('File size must be less than 30 MB.', {
      exact: false,
    })
  ).toBeVisible()
  expect(screen.getByLabelText('Name')).toHaveValue('Media')
  expect(screen.getByRole('button', { name: 'Add asset' })).toBeEnabled()
  expect(post).not.toHaveBeenCalled()
})

test('group creation uses GroupType and selected owner', async () => {
  const user = userEvent.setup()
  const post = vi
    .spyOn(api, 'post')
    .mockResolvedValue({ data: { success: true, data: { Id: 'group-new' } } })
  render(
    <QueryClientProvider client={client}>
      <AssetGroupDialog
        project='studio'
        scope={{ userId: 23 }}
        onClose={vi.fn()}
      />
    </QueryClientProvider>
  )
  await user.type(screen.getByLabelText('Name'), 'People')
  await user.click(screen.getByRole('button', { name: 'Save' }))
  await waitFor(() => expect(post).toHaveBeenCalledOnce())
  expect(post).toHaveBeenCalledWith(
    '/api/assets/actions',
    { ProjectName: 'studio', GroupType: 'AIGC', Name: 'People' },
    expect.objectContaining({
      params: expect.objectContaining({
        Action: 'CreateAssetGroup',
        user_id: 23,
      }),
    })
  )
})

test('group selection survives refresh and failed deletion leaves confirmation open', async () => {
  const user = userEvent.setup()
  const post = vi
    .spyOn(api, 'post')
    .mockImplementation(async (_url, _body, options) => {
      if (
        (options?.params as { Action?: string } | undefined)?.Action ===
        'DeleteAsset'
      ) {
        return { data: { success: false, message: 'Asset is in use' } }
      }
      if (
        (options?.params as { Action?: string } | undefined)?.Action ===
        'ListAssetGroups'
      ) {
        return {
          data: {
            success: true,
            data: {
              Items: [group, { ...group, Id: 'group-two', Name: 'Products' }],
            },
          },
        }
      }
      return {
        data: {
          success: true,
          data: {
            Items: [
              {
                Id: 'asset-one',
                GroupId: 'group-two',
                ProjectName: 'default',
                Name: 'Product',
                Status: 'Active',
                AssetType: 'Image',
                URL: 'https://example.com/product.png',
              },
            ],
            TotalCount: 1,
          },
        },
      }
    })
  render(
    <QueryClientProvider client={client}>
      <AssetBrowser project='default' scope={{}} admin={false} />
    </QueryClientProvider>
  )
  await screen.findByRole('option', { name: 'Products' })
  await user.selectOptions(
    await screen.findByLabelText('Asset group'),
    'group-two'
  )
  await act(() => client.invalidateQueries({ queryKey: ['assets'] }))
  expect(screen.getByLabelText('Asset group')).toHaveValue('group-two')
  await waitFor(() => expect(client.isFetching()).toBe(0))
  await user.click(
    await screen.findByRole('button', { name: 'Delete Product' })
  )
  const dialog = await screen.findByRole('alertdialog')
  await user.click(within(dialog).getByRole('button', { name: 'Delete' }))
  expect(await within(dialog).findByRole('alert')).toHaveTextContent(
    'Asset is in use'
  )
  expect(screen.getByRole('alertdialog')).toBeVisible()
  expect(post).toHaveBeenCalledWith(
    '/api/assets/actions',
    { ProjectName: 'default', Id: 'asset-one' },
    expect.objectContaining({
      params: expect.objectContaining({ Action: 'DeleteAsset' }),
    })
  )
})

test('group deletion warns that all assets and their URIs are removed before confirmation', async () => {
  const user = userEvent.setup()
  let deleted = false
  const post = vi
    .spyOn(api, 'post')
    .mockImplementation(async (_url, _body, options) => {
      const action = (options?.params as { Action?: string } | undefined)
        ?.Action
      if (action === 'DeleteAssetGroup') {
        deleted = true
        return { data: { success: true, data: {} } }
      }
      return {
        data: {
          success: true,
          data: {
            Items: action === 'ListAssetGroups' && !deleted ? [group] : [],
            TotalCount: 0,
          },
        },
      }
    })
  render(
    <QueryClientProvider client={client}>
      <AssetBrowser project='default' scope={{ userId: 23 }} admin />
    </QueryClientProvider>
  )
  await screen.findByRole('option', { name: 'Avatars' })
  await user.click(screen.getByRole('button', { name: 'Group actions' }))
  await user.click(screen.getByRole('menuitem', { name: 'Delete group' }))
  const dialog = screen.getByRole('alertdialog')
  expect(dialog).toHaveTextContent(
    'Delete Avatars and all assets in this group? Their asset URIs will no longer work.'
  )
  expect(deleted).toBe(false)
  await user.click(within(dialog).getByRole('button', { name: 'Cancel' }))
  expect(deleted).toBe(false)
  await user.click(screen.getByRole('button', { name: 'Group actions' }))
  await user.click(screen.getByRole('menuitem', { name: 'Delete group' }))
  await user.click(
    within(screen.getByRole('alertdialog')).getByRole('button', {
      name: 'Delete',
    })
  )
  await waitFor(() => expect(deleted).toBe(true))
  expect(post).toHaveBeenCalledWith(
    '/api/assets/actions',
    { ProjectName: 'default', Id: group.Id },
    expect.objectContaining({
      params: expect.objectContaining({
        Action: 'DeleteAssetGroup',
        user_id: 23,
      }),
    })
  )
})

test.each(['Load more', 'Refresh'])(
  'failed group %s preserves the list and selection and retries the failed request',
  async (action) => {
    const user = userEvent.setup()
    let groupRequests = 0
    const groupRequestBodies: unknown[] = []
    vi.spyOn(api, 'post').mockImplementation(async (_url, body, options) => {
      if (
        (options?.params as { Action?: string } | undefined)?.Action ===
        'ListAssetGroups'
      ) {
        groupRequestBodies.push(body)
        groupRequests += 1
        if (groupRequests === 2) {
          return { data: { success: false, message: 'Groups unavailable' } }
        }
        return {
          data: {
            success: true,
            data: {
              Items: (body as { NextToken?: string }).NextToken
                ? [{ ...group, Id: 'group-two', Name: 'Products' }]
                : [group],
              NextToken: groupRequests === 1 ? 'next-page' : undefined,
            },
          },
        }
      }
      return {
        data: {
          success: true,
          data: {
            Items: [
              {
                Id: 'asset-one',
                Name: 'Avatar',
                GroupId: group.Id,
                ProjectName: 'default',
                Status: 'Active',
                AssetType: 'Image',
                URL: '',
              },
            ],
            TotalCount: 1,
          },
        },
      }
    })
    render(
      <QueryClientProvider client={client}>
        <AssetBrowser project='default' scope={{}} admin={false} />
      </QueryClientProvider>
    )
    expect(await screen.findByText('Avatar')).toBeVisible()
    await user.click(screen.getByRole('button', { name: action }))
    expect(await screen.findByText('Groups unavailable')).toBeVisible()
    expect(screen.getByLabelText('Asset group')).toHaveValue(group.Id)
    expect(screen.getByText('Avatar')).toBeVisible()
    await user.click(screen.getByRole('button', { name: 'Retry' }))
    await waitFor(() =>
      expect(screen.queryByText('Groups unavailable')).not.toBeInTheDocument()
    )
    expect(groupRequests).toBe(3)
    expect(groupRequestBodies[2]).toEqual(groupRequestBodies[1])
    expect(screen.getByLabelText('Asset group')).toHaveValue(group.Id)
    if (action === 'Load more') {
      expect(
        screen.getByRole('option', { name: 'Products' })
      ).toBeInTheDocument()
    }
  }
)

test.each([false, true])(
  'all-user browsing disables mutations and gates replicas by channel read access (%s)',
  async (canReadChannel) => {
    const user = userEvent.setup()
    useAuthStore.getState().auth.setUser({
      id: 1,
      username: 'admin',
      role: ROLE.ADMIN,
      permissions: { admin_permissions: { channel: { read: canReadChannel } } },
    })
    const get = vi
      .spyOn(api, 'get')
      .mockResolvedValue({ data: { success: true, data: [] } })
    vi.spyOn(api, 'post').mockImplementation(async (_url, _body, options) => ({
      data: {
        success: true,
        data:
          (options?.params as { Action?: string } | undefined)?.Action ===
          'ListAssetGroups'
            ? { Items: [group] }
            : {
                Items: [
                  {
                    Id: 'asset-one',
                    Name: 'Avatar',
                    GroupId: group.Id,
                    ProjectName: 'default',
                    UserId: 23,
                    Status: 'Active',
                    AssetType: 'Image',
                    URL: '',
                  },
                ],
                TotalCount: 1,
              },
      },
    }))
    render(
      <QueryClientProvider client={client}>
        <AssetBrowser project='default' scope={{ allUsers: true }} admin />
      </QueryClientProvider>
    )
    await screen.findByRole('option', { name: 'Avatars (23)' })
    expect(screen.getByRole('button', { name: 'Add asset' })).toBeDisabled()
    expect(
      await screen.findByRole('button', { name: 'Delete Avatar' })
    ).toBeDisabled()
    await user.click(screen.getByRole('button', { name: 'Group actions' }))
    expect(
      screen.getByRole('menuitem', { name: 'Create asset group' })
    ).toHaveAttribute('aria-disabled', 'true')
    expect(
      screen.getByRole('menuitem', { name: 'Delete group' })
    ).toHaveAttribute('aria-disabled', 'true')
    await user.keyboard('{Escape}')
    if (!canReadChannel) {
      expect(
        screen.queryByRole('button', { name: 'Channel replicas' })
      ).not.toBeInTheDocument()
      expect(get).not.toHaveBeenCalled()
      return
    }
    await user.click(screen.getByRole('button', { name: 'Channel replicas' }))
    await waitFor(() =>
      expect(get).toHaveBeenCalledWith(
        '/api/assets/asset-one/replicas',
        expect.objectContaining({ params: { user_id: 23 } })
      )
    )
  }
)

test('a disabled asset library exposes no creation or browsing requests', async () => {
  vi.spyOn(api, 'get').mockResolvedValue({
    data: { success: true, data: { enabled: false } },
  })
  const post = vi.spyOn(api, 'post')
  render(
    <QueryClientProvider client={client}>
      <Assets />
    </QueryClientProvider>
  )
  expect(await screen.findByText('Asset library is disabled')).toBeVisible()
  expect(
    screen.queryByRole('button', { name: 'Create asset group' })
  ).not.toBeInTheDocument()
  expect(post).not.toHaveBeenCalled()
})

test.each([
  [ROLE.SUPER_ADMIN, {}, true, true],
  [ROLE.ADMIN, { read: true, sensitive_write: true }, true, false],
  [ROLE.ADMIN, { read: true, sensitive_write: false }, false, false],
  [ROLE.USER, {}, false, false],
])(
  'disabled library keeps only authorized settings accessible for role %s and permissions %j',
  async (role, channel, channelsVisible, storageVisible) => {
    useAuthStore.getState().auth.setUser({
      id: 1,
      username: 'viewer',
      role,
      permissions: { admin_permissions: { channel } },
    })
    vi.spyOn(api, 'get').mockResolvedValue({
      data: { success: true, data: { enabled: false } },
    })
    const post = vi.spyOn(api, 'post')
    await renderAssetsPage()
    expect(await screen.findByText('Asset library is disabled')).toBeVisible()
    expect(
      Boolean(screen.queryByRole('button', { name: 'Channel settings' }))
    ).toBe(channelsVisible)
    const storage = screen.queryByRole('button', { name: 'Storage settings' })
    expect(Boolean(storage)).toBe(storageVisible)
    if (storage) {
      expect(storage).toHaveAttribute(
        'href',
        '/system-settings/operations/assets'
      )
    }
    expect(post).not.toHaveBeenCalled()
  }
)

test('draft ownership changes do not change the upload target until applied', async () => {
  const user = userEvent.setup()
  useAuthStore
    .getState()
    .auth.setUser({ id: 1, username: 'admin', role: ROLE.ADMIN })
  vi.spyOn(api, 'get').mockResolvedValue({
    data: {
      success: true,
      data: {
        enabled: true,
        size_limits_mb: { image: 30, video: 200, audio: 15 },
      },
    },
  })
  const post = vi
    .spyOn(api, 'post')
    .mockImplementation(async (_url, body, options) => {
      const action = (options?.params as { Action?: string } | undefined)
        ?.Action
      if (action === 'ListAssetGroups') {
        return {
          data: {
            success: true,
            data: {
              Items: [
                {
                  ...group,
                  ProjectName: (body as { ProjectName: string }).ProjectName,
                },
              ],
            },
          },
        }
      }
      return {
        data: {
          success: true,
          data: { Items: [], TotalCount: 0, Id: 'asset-one' },
        },
      }
    })
  await renderAssetsPage()
  await screen.findByRole('option', { name: 'Avatars' })
  await user.selectOptions(screen.getByLabelText('Owner'), 'user')
  await user.type(screen.getByLabelText('User ID'), '23')
  await user.clear(screen.getByLabelText('Project name'))
  await user.type(screen.getByLabelText('Project name'), 'studio')
  await user.click(screen.getByRole('button', { name: 'Add asset' }))
  const dialog = screen.getByRole('dialog')
  await user.type(within(dialog).getByLabelText('Name'), 'Avatar')
  await user.type(
    within(dialog).getByLabelText('Asset URL'),
    'https://example.com/avatar.png'
  )
  await user.click(within(dialog).getByRole('button', { name: 'Add asset' }))
  await waitFor(() =>
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  )
  expect(post).toHaveBeenCalledWith(
    '/api/assets/actions',
    expect.objectContaining({ ProjectName: 'default', Name: 'Avatar' }),
    expect.objectContaining({
      params: expect.objectContaining({
        Action: 'CreateAsset',
        user_id: undefined,
      }),
    })
  )
  await user.click(screen.getByRole('button', { name: 'Apply' }))
  await waitFor(() =>
    expect(post).toHaveBeenCalledWith(
      '/api/assets/actions',
      expect.objectContaining({ ProjectName: 'studio' }),
      expect.objectContaining({
        params: expect.objectContaining({
          Action: 'ListAssetGroups',
          user_id: 23,
        }),
      })
    )
  )
})

test('group actions are available from one menu and an empty library offers group creation', async () => {
  const user = userEvent.setup()
  vi.spyOn(api, 'post').mockResolvedValue({
    data: { success: true, data: { Items: [] } },
  })
  render(
    <QueryClientProvider client={client}>
      <AssetBrowser project='default' scope={{}} admin={false} />
    </QueryClientProvider>
  )
  expect(
    await screen.findByRole('button', { name: 'Create asset group' })
  ).toBeVisible()
  expect(screen.getByRole('button', { name: 'Add asset' })).toBeDisabled()
  await user.click(screen.getByRole('button', { name: 'Group actions' }))
  expect(
    screen.getByRole('menuitem', { name: 'Create asset group' })
  ).toBeEnabled()
  expect(screen.getByRole('menuitem', { name: 'Rename' })).toHaveAttribute(
    'aria-disabled',
    'true'
  )
  await user.keyboard('{Escape}')
  await user.click(screen.getByRole('button', { name: 'Create asset group' }))
  expect(
    await screen.findByRole('dialog', { name: 'Create asset group' })
  ).toBeVisible()
})

test('a failed list refresh keeps existing assets and pagination available', async () => {
  const user = userEvent.setup()
  let failList = false
  vi.spyOn(api, 'post').mockImplementation(async (_url, _body, options) => {
    if (
      (options?.params as { Action?: string } | undefined)?.Action ===
      'ListAssetGroups'
    ) {
      return { data: { success: true, data: { Items: [group] } } }
    }
    if (failList) {
      return { data: { success: false, message: 'Assets unavailable' } }
    }
    return {
      data: {
        success: true,
        data: {
          Items: [
            {
              Id: 'asset-one',
              Name: 'Avatar',
              ProjectName: 'default',
              GroupId: group.Id,
              Status: 'Active',
              AssetType: 'Image',
              URL: '',
            },
          ],
          TotalCount: 21,
        },
      },
    }
  })
  render(
    <QueryClientProvider client={client}>
      <AssetBrowser project='default' scope={{}} admin={false} />
    </QueryClientProvider>
  )
  expect(await screen.findByText('Avatar')).toBeVisible()
  failList = true
  await user.click(screen.getByRole('button', { name: 'Refresh' }))
  expect(await screen.findByRole('alert')).toHaveTextContent(
    'Assets unavailable'
  )
  expect(screen.getByText('Avatar')).toBeVisible()
  expect(screen.getByRole('button', { name: 'Go to next page' })).toBeEnabled()
  failList = false
  await user.click(screen.getByRole('button', { name: 'Refresh' }))
  await waitFor(() =>
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  )
})

test('switching library types isolates group filters, asset queries and mutations', async () => {
  const user = userEvent.setup()
  const human = {
    ...group,
    Id: 'group-person',
    Name: 'Person',
    GroupType: 'LivenessFace',
    Status: 'active',
  }
  const post = vi
    .spyOn(api, 'post')
    .mockImplementation(async (_url, body, options) => {
      const action = (options?.params as { Action?: string })?.Action
      const filter = (body as { Filter?: { GroupType?: string } })?.Filter
      return {
        data: {
          success: true,
          data: {
            Items:
              action === 'ListAssetGroups'
                ? [filter?.GroupType === 'LivenessFace' ? human : group]
                : [],
            TotalCount: 0,
          },
        },
      }
    })
  render(
    <QueryClientProvider client={client}>
      <AssetBrowser project='studio' scope={{ userId: 23 }} admin />
    </QueryClientProvider>
  )
  await screen.findByRole('option', { name: 'Avatars' })
  await user.selectOptions(
    screen.getByLabelText('Library type'),
    'LivenessFace'
  )
  await screen.findByRole('option', { name: 'Person' })
  expect(screen.getByLabelText('Asset group')).toHaveValue('group-person')
  expect(
    screen.queryByRole('option', { name: 'Avatars' })
  ).not.toBeInTheDocument()
  await waitFor(() =>
    expect(post).toHaveBeenCalledWith(
      '/api/assets/actions',
      expect.objectContaining({
        Filter: { GroupType: 'LivenessFace', GroupIds: ['group-person'] },
      }),
      expect.objectContaining({
        params: expect.objectContaining({ Action: 'ListAssets', user_id: 23 }),
      })
    )
  )
  await user.click(screen.getByRole('button', { name: 'Group actions' }))
  expect(
    screen.getByRole('menuitem', { name: 'Verify a person' })
  ).toBeEnabled()
  expect(
    screen.queryByRole('menuitem', { name: 'Create asset group' })
  ).not.toBeInTheDocument()
  await user.keyboard('{Escape}')
  await user.selectOptions(screen.getByLabelText('Library type'), 'AIGC')
  expect(await screen.findByRole('option', { name: 'Avatars' })).toBeVisible()
  expect(screen.getByLabelText('Asset group')).toHaveValue(group.Id)
})

test('verification creation retries the same request key and exposes only the secured H5 link', async () => {
  const user = userEvent.setup()
  vi.mocked(api.get).mockResolvedValue({ data: { success: true, data: [] } })
  const post = vi
    .spyOn(api, 'post')
    .mockRejectedValueOnce(new Error('Connection interrupted'))
    .mockResolvedValue({
      data: {
        success: true,
        data: {
          BytedToken: 'validation-na-one',
          Name: 'Alice',
          Status: 'pending',
          ChannelId: 7,
          ExpiresAt: Date.now() / 1000 + 1800,
          H5Link: 'https://verify.example/face?token=upstream-private',
          CallbackURL:
            'https://gateway.example/api/assets/validation/callback/private',
        },
      },
    })
  const persist = vi.spyOn(localStorage, 'setItem')
  render(
    <QueryClientProvider client={client}>
      <AssetValidationDialog
        project='studio'
        scope={{ userId: 23 }}
        onClose={vi.fn()}
        onCreated={vi.fn()}
      />
    </QueryClientProvider>
  )
  await user.type(screen.getByLabelText('Name'), 'Alice')
  await user.click(screen.getByRole('button', { name: 'Start verification' }))
  expect(await screen.findByRole('alert')).toHaveTextContent(
    'Connection interrupted'
  )
  expect(screen.getByLabelText('Name')).toHaveValue('Alice')
  await user.click(screen.getByRole('button', { name: 'Start verification' }))
  const link = await screen.findByRole('button', { name: 'Open verification' })
  expect(link).toHaveAttribute(
    'href',
    'https://verify.example/face?token=upstream-private&lng=en'
  )
  expect(link).toHaveAttribute('rel', 'noopener noreferrer')
  expect(link).toHaveAttribute('referrerpolicy', 'no-referrer')
  expect(
    screen.getByRole('button', { name: 'Copy verification link' })
  ).toBeVisible()
  expect(post.mock.calls[0][1]).toEqual(post.mock.calls[1][1])
  expect(post.mock.calls[1][1]).toEqual({
    ProjectName: 'studio',
    Name: 'Alice',
    IdempotencyKey: expect.any(String),
  })
  expect(post.mock.calls[1][2]).toEqual(
    expect.objectContaining({
      params: expect.objectContaining({
        Action: 'CreateVisualValidateSession',
        user_id: 23,
      }),
    })
  )
  expect(persist).not.toHaveBeenCalled()
})

test('a saved verification resumes through a write action and selects its created group', async () => {
  const user = userEvent.setup()
  const created = vi.fn()
  vi.mocked(api.get).mockResolvedValue({
    data: {
      success: true,
      data: [
        {
          SessionId: 'validation-na-one',
          Name: 'Alice',
          Status: 'pending',
          ChannelId: 7,
          ExpiresAt: Date.now() / 1000 + 1800,
        },
      ],
    },
  })
  const post = vi
    .spyOn(api, 'post')
    .mockResolvedValueOnce({
      data: {
        success: true,
        data: {
          BytedToken: 'validation-na-one',
          Name: 'Alice',
          Status: 'pending',
          ChannelId: 7,
          ExpiresAt: Date.now() / 1000 + 1800,
          H5Link: 'https://verify.example/face',
        },
      },
    })
    .mockResolvedValueOnce({
      data: {
        success: true,
        data: {
          BytedToken: 'validation-na-one',
          Name: 'Alice',
          Status: 'created',
          GroupId: 'group-na-person',
        },
      },
    })
  render(
    <QueryClientProvider client={client}>
      <AssetValidationDialog
        project='studio'
        scope={{ userId: 23 }}
        onClose={vi.fn()}
        onCreated={created}
      />
    </QueryClientProvider>
  )
  const resume = await screen.findByRole('button', {
    name: 'Continue verification for Alice',
  })
  expect(post).not.toHaveBeenCalled()
  expect(
    screen.queryByRole('button', { name: 'Open verification' })
  ).not.toBeInTheDocument()
  await user.click(resume)
  expect(
    await screen.findByRole('button', { name: 'Open verification' })
  ).toBeVisible()
  expect(api.get).toHaveBeenCalledWith(
    '/api/assets/validation/sessions',
    expect.objectContaining({ params: { project_name: 'studio', user_id: 23 } })
  )
  expect(post).toHaveBeenCalledWith(
    '/api/assets/actions',
    { ProjectName: 'studio', BytedToken: 'validation-na-one' },
    expect.objectContaining({
      params: expect.objectContaining({
        Action: 'GetVisualValidateResult',
        user_id: 23,
      }),
    })
  )
  await user.click(screen.getByRole('button', { name: 'Check verification' }))
  await waitFor(() => expect(created).toHaveBeenCalledWith('group-na-person'))
  expect(
    screen.queryByRole('button', { name: 'Open verification' })
  ).not.toBeInTheDocument()
})

test.each([
  { Status: 'expired', ExpiresAt: 1, H5Link: 'https://verify.example/expired' },
  { Status: 'pending', ExpiresAt: 1, H5Link: 'https://verify.example/expired' },
  { Status: 'pending', ExpiresAt: 9999999999, H5Link: 'javascript:alert(1)' },
])(
  'expired or unsafe verification links are never offered (%j)',
  async (result) => {
    const user = userEvent.setup()
    vi.mocked(api.get).mockResolvedValue({
      data: {
        success: true,
        data: [
          { SessionId: 'validation-na-one', Name: 'Alice', Status: 'pending' },
        ],
      },
    })
    vi.spyOn(api, 'post').mockResolvedValue({
      data: {
        success: true,
        data: { ...result, BytedToken: 'validation-na-one', Name: 'Alice' },
      },
    })
    render(
      <QueryClientProvider client={client}>
        <AssetValidationDialog
          project='default'
          scope={{}}
          onClose={vi.fn()}
          onCreated={vi.fn()}
        />
      </QueryClientProvider>
    )
    await user.click(
      await screen.findByRole('button', {
        name: 'Continue verification for Alice',
      })
    )
    expect(
      await screen.findByRole('button', { name: 'Check verification' })
    ).toBeVisible()
    expect(
      screen.queryByRole('button', { name: 'Open verification' })
    ).not.toBeInTheDocument()
    expect(
      screen.queryByRole('button', { name: 'Copy verification link' })
    ).not.toBeInTheDocument()
  }
)

test.each([
  ['zhCN', 'zh'],
  ['zhTW', 'zh-Hant'],
  ['zh-Hant', 'zh-Hant'],
  ['en', 'en'],
  ['fr', 'en'],
  ['ru', 'en'],
  ['ja', 'en'],
  ['vi', 'en'],
  ['invalid_language', 'en'],
])(
  'verification language %s maps to provider language %s',
  (language, expected) => {
    expect(
      assetValidationH5Link(
        'https://verify.example/face?token=preserved&lng=zh',
        language
      )
    ).toBe(`https://verify.example/face?token=preserved&lng=${expected}`)
  }
)

test.each([
  { status: 'delete_pending', cancel: false, retry: false },
  { status: 'delete_unknown', cancel: false, retry: true },
  { status: 'delete_blocked', cancel: true, retry: true },
])(
  'human group $status freezes writes and exposes only valid recovery actions',
  async ({ status, cancel, retry }) => {
    const user = userEvent.setup()
    const human = {
      ...group,
      GroupType: 'LivenessFace',
      Status: status,
      Error: {
        Code: 'UpstreamDenied',
        Message: 'Provider authorization is active',
      },
    }
    vi.spyOn(api, 'post').mockImplementation(async (_url, _body, options) => ({
      data: {
        success: true,
        data: {
          Items:
            (options?.params as { Action?: string })?.Action ===
            'ListAssetGroups'
              ? [human]
              : [replicaAsset],
          TotalCount: 1,
        },
      },
    }))
    render(
      <QueryClientProvider client={client}>
        <AssetBrowser project='default' scope={{}} admin={false} />
      </QueryClientProvider>
    )
    await user.selectOptions(
      screen.getByLabelText('Library type'),
      'LivenessFace'
    )
    expect(
      await screen.findByText(
        'UpstreamDenied: Provider authorization is active'
      )
    ).toBeVisible()
    expect(screen.getByRole('button', { name: 'Add asset' })).toBeDisabled()
    expect(
      await screen.findByRole('button', { name: 'Delete Avatar' })
    ).toBeDisabled()
    expect(
      screen.queryByRole('button', { name: 'Copy asset URI' })
    ).not.toBeInTheDocument()
    expect(
      Boolean(screen.queryByRole('button', { name: 'Cancel deletion' }))
    ).toBe(cancel)
    expect(
      Boolean(screen.queryByRole('button', { name: 'Retry deletion' }))
    ).toBe(retry)
    await user.click(screen.getByRole('button', { name: 'Group actions' }))
    expect(screen.getByRole('menuitem', { name: 'Rename' })).toHaveAttribute(
      'aria-disabled',
      'true'
    )
    expect(
      screen.getByRole('menuitem', { name: 'Delete group' })
    ).toHaveAttribute('aria-disabled', 'true')
  }
)

test('accepted human deletion stays visible as pending and does not report completion', async () => {
  const user = userEvent.setup()
  const success = vi.spyOn(toast, 'success')
  let status = 'active'
  vi.spyOn(api, 'post').mockImplementation(async (_url, _body, options) => {
    const action = (options?.params as { Action?: string })?.Action
    if (action === 'DeleteAssetGroup') {
      status = 'delete_pending'
      return {
        status: 202,
        data: { success: true, data: { Id: group.Id, Status: status } },
      }
    }
    return {
      data: {
        success: true,
        data: {
          Items:
            action === 'ListAssetGroups'
              ? [{ ...group, GroupType: 'LivenessFace', Status: status }]
              : [],
          TotalCount: 0,
        },
      },
    }
  })
  render(
    <QueryClientProvider client={client}>
      <AssetBrowser project='default' scope={{}} admin={false} />
    </QueryClientProvider>
  )
  await user.selectOptions(
    screen.getByLabelText('Library type'),
    'LivenessFace'
  )
  await screen.findByRole('option', { name: 'Avatars' })
  await user.click(screen.getByRole('button', { name: 'Group actions' }))
  await user.click(screen.getByRole('menuitem', { name: 'Delete group' }))
  await user.click(
    within(screen.getByRole('alertdialog')).getByRole('button', {
      name: 'Delete',
    })
  )
  expect(await screen.findByText('Deletion pending')).toBeVisible()
  expect(screen.getByLabelText('Asset group')).toHaveValue(group.Id)
  expect(screen.getByRole('button', { name: 'Add asset' })).toBeDisabled()
  expect(success).toHaveBeenCalledWith('Group deletion requested')
  expect(success).not.toHaveBeenCalledWith('Deleted successfully')
})

test('an open verification polls once, pauses on failure and keeps manual recovery available', async () => {
  const user = userEvent.setup()
  const created = vi.fn()
  const scheduled: (() => void)[] = []
  const setTimeout = window.setTimeout.bind(window)
  vi.spyOn(window, 'setTimeout').mockImplementation(
    (callback, delay, ...args) => {
      if (delay === 10_000 && typeof callback === 'function') {
        scheduled.push(() => callback(...args))
        return setTimeout(() => undefined, 0) as unknown as ReturnType<
          typeof window.setTimeout
        >
      }
      return setTimeout(callback, delay, ...args) as unknown as ReturnType<
        typeof window.setTimeout
      >
    }
  )
  vi.mocked(api.get).mockResolvedValue({
    data: {
      success: true,
      data: [
        { SessionId: 'validation-na-one', Name: 'Alice', Status: 'pending' },
      ],
    },
  })
  const post = vi
    .spyOn(api, 'post')
    .mockResolvedValueOnce({
      data: {
        success: true,
        data: {
          BytedToken: 'validation-na-one',
          Name: 'Alice',
          Status: 'pending',
          ExpiresAt: Date.now() / 1000 + 1800,
          H5Link: 'https://verify.example/face',
        },
      },
    })
    .mockRejectedValueOnce(new Error('Verification temporarily unavailable'))
    .mockResolvedValueOnce({
      data: {
        success: true,
        data: {
          BytedToken: 'validation-na-one',
          Name: 'Alice',
          Status: 'created',
          GroupId: 'group-na-person',
        },
      },
    })
  render(
    <QueryClientProvider client={client}>
      <AssetValidationDialog
        project='default'
        scope={{}}
        onClose={vi.fn()}
        onCreated={created}
      />
    </QueryClientProvider>
  )
  await user.click(
    await screen.findByRole('button', {
      name: 'Continue verification for Alice',
    })
  )
  expect(
    await screen.findByRole('button', { name: 'Open verification' })
  ).toBeVisible()
  expect(scheduled).toHaveLength(1)
  await act(async () => scheduled.shift()?.())
  expect(await screen.findByRole('alert')).toHaveTextContent(
    'Verification temporarily unavailable'
  )
  expect(post).toHaveBeenCalledTimes(2)
  expect(scheduled).toHaveLength(0)
  await user.click(screen.getByRole('button', { name: 'Check verification' }))
  await waitFor(() => expect(created).toHaveBeenCalledWith('group-na-person'))
})

test('cancelling a blocked human deletion uses its owner and unfreezes the group after success', async () => {
  const user = userEvent.setup()
  let status = 'delete_blocked'
  const post = vi
    .spyOn(api, 'post')
    .mockImplementation(async (_url, _body, options) => {
      const action = (options?.params as { Action?: string })?.Action
      if (action === 'CancelAssetGroupDeletion') {
        status = 'active'
        return { data: { success: true, data: {} } }
      }
      return {
        data: {
          success: true,
          data: {
            Items:
              action === 'ListAssetGroups'
                ? [{ ...group, GroupType: 'LivenessFace', Status: status }]
                : [],
            TotalCount: 0,
          },
        },
      }
    })
  render(
    <QueryClientProvider client={client}>
      <AssetBrowser project='default' scope={{ userId: 23 }} admin />
    </QueryClientProvider>
  )
  await user.selectOptions(
    screen.getByLabelText('Library type'),
    'LivenessFace'
  )
  await user.click(
    await screen.findByRole('button', { name: 'Cancel deletion' })
  )
  await waitFor(() =>
    expect(screen.getByRole('button', { name: 'Add asset' })).toBeEnabled()
  )
  expect(post).toHaveBeenCalledWith(
    '/api/assets/actions',
    { ProjectName: 'default', Id: group.Id },
    expect.objectContaining({
      params: expect.objectContaining({
        Action: 'CancelAssetGroupDeletion',
        user_id: 23,
      }),
    })
  )
})

test.each(['removed', 'delete_pending'] as const)(
  'an open upload closes when its original group becomes %s and never moves to another person',
  async (change) => {
    const user = userEvent.setup()
    let changed = false
    vi.spyOn(api, 'post').mockImplementation(async (_url, _body, options) => {
      const action = (options?.params as { Action?: string })?.Action
      const original: AssetGroup = { ...group, GroupType: 'LivenessFace' }
      const next = { ...original, Id: 'group-next', Name: 'Another person' }
      let items = [original, next]
      if (changed) {
        items =
          change === 'removed'
            ? [next]
            : [{ ...original, Status: 'delete_pending' }, next]
      }
      return {
        data: {
          success: true,
          data: {
            Items: action === 'ListAssetGroups' ? items : [],
            TotalCount: 0,
          },
        },
      }
    })
    render(
      <QueryClientProvider client={client}>
        <AssetBrowser project='default' scope={{}} admin={false} />
      </QueryClientProvider>
    )
    await user.selectOptions(
      screen.getByLabelText('Library type'),
      'LivenessFace'
    )
    await screen.findByRole('option', { name: 'Avatars' })
    await user.click(screen.getByRole('button', { name: 'Add asset' }))
    expect(screen.getByRole('dialog')).toBeInTheDocument()
    changed = true
    await act(() => client.invalidateQueries({ queryKey: ['assets'] }))
    await waitFor(() =>
      expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    )
    changed = false
    await act(() => client.invalidateQueries({ queryKey: ['assets'] }))
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  }
)

test('background group refresh never retargets an open delete confirmation', async () => {
  const user = userEvent.setup()
  let removedFromList = false
  const post = vi
    .spyOn(api, 'post')
    .mockImplementation(async (_url, _body, options) => {
      const action = (options?.params as { Action?: string })?.Action
      if (action === 'DeleteAssetGroup') {
        return {
          data: { success: false, message: 'Original group unavailable' },
        }
      }
      const next = {
        ...group,
        Id: 'group-next',
        Name: 'Another person',
        GroupType: 'LivenessFace',
      }
      const items = removedFromList
        ? [next]
        : [{ ...group, GroupType: 'LivenessFace' }, next]
      return {
        data: {
          success: true,
          data: {
            Items: action === 'ListAssetGroups' ? items : [],
            TotalCount: 0,
          },
        },
      }
    })
  render(
    <QueryClientProvider client={client}>
      <AssetBrowser project='default' scope={{}} admin={false} />
    </QueryClientProvider>
  )
  await user.selectOptions(
    screen.getByLabelText('Library type'),
    'LivenessFace'
  )
  await screen.findByRole('option', { name: 'Avatars' })
  await user.click(screen.getByRole('button', { name: 'Group actions' }))
  await user.click(screen.getByRole('menuitem', { name: 'Delete group' }))
  removedFromList = true
  await act(() => client.invalidateQueries({ queryKey: ['assets'] }))
  const dialog = screen.getByRole('alertdialog')
  expect(dialog).toHaveTextContent(
    'Delete Avatars and all assets in this group?'
  )
  await user.click(within(dialog).getByRole('button', { name: 'Delete' }))
  expect(await within(dialog).findByRole('alert')).toHaveTextContent(
    'Original group unavailable'
  )
  expect(post).toHaveBeenCalledWith(
    '/api/assets/actions',
    { ProjectName: 'default', Id: 'group-one' },
    expect.objectContaining({
      params: expect.objectContaining({ Action: 'DeleteAssetGroup' }),
    })
  )
})

test('a verification link disappears at its deadline even when result polling has failed', async () => {
  const user = userEvent.setup()
  let expire: (() => void) | undefined
  const timer = window.setTimeout.bind(window)
  vi.spyOn(window, 'setTimeout').mockImplementation(
    (callback, delay, ...args) => {
      if (delay && delay > 10_000 && typeof callback === 'function') {
        expire = () => callback(...args)
        return timer(() => undefined, 0) as unknown as ReturnType<
          typeof window.setTimeout
        >
      }
      return timer(callback, delay, ...args) as unknown as ReturnType<
        typeof window.setTimeout
      >
    }
  )
  vi.mocked(api.get).mockResolvedValue({
    data: {
      success: true,
      data: [
        { SessionId: 'validation-na-one', Name: 'Alice', Status: 'pending' },
      ],
    },
  })
  vi.spyOn(api, 'post')
    .mockResolvedValueOnce({
      data: {
        success: true,
        data: {
          BytedToken: 'validation-na-one',
          Name: 'Alice',
          Status: 'pending',
          ExpiresAt: Date.now() / 1000 + 1800,
          H5Link: 'https://verify.example/face',
        },
      },
    })
    .mockRejectedValueOnce(new Error('Permission revoked'))
  render(
    <QueryClientProvider client={client}>
      <AssetValidationDialog
        project='default'
        scope={{}}
        onClose={vi.fn()}
        onCreated={vi.fn()}
      />
    </QueryClientProvider>
  )
  await user.click(
    await screen.findByRole('button', {
      name: 'Continue verification for Alice',
    })
  )
  expect(
    await screen.findByRole('button', { name: 'Open verification' })
  ).toBeVisible()
  await user.click(screen.getByRole('button', { name: 'Check verification' }))
  expect(await screen.findByRole('alert')).toHaveTextContent(
    'Permission revoked'
  )
  expect(expire).toBeDefined()
  act(() => expire?.())
  expect(
    screen.queryByRole('button', { name: 'Open verification' })
  ).not.toBeInTheDocument()
  expect(
    screen.getByText(
      'This verification link has expired. Start a new verification if no group was created.'
    )
  ).toBeVisible()
})
