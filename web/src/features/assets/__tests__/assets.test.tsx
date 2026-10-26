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
import { act, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, afterEach, expect, test, vi } from 'vitest'

import { api } from '@/lib/api'
import { ROLE } from '@/lib/roles'
import { useAuthStore } from '@/stores/auth-store'

import { AssetBrowser } from '../components/asset-browser'
import { AssetCreateDialog } from '../components/asset-create-dialog'
import { AssetGroupDialog } from '../components/asset-group-dialog'
import { Assets } from '../index'
import type { AssetGroup } from '../types'

const group: AssetGroup = {
  Id: 'group-one',
  Name: 'Avatars',
  Description: '',
  ProjectName: 'default',
  GroupType: 'AIGC',
  UserId: 23,
}
let client: QueryClient

beforeEach(() => {
  client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
})
afterEach(() => {
  client.clear()
  useAuthStore.getState().auth.reset()
})

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
  await user.click(await screen.findByRole('button', { name: 'Delete group' }))
  const dialog = screen.getByRole('alertdialog')
  expect(dialog).toHaveTextContent(
    'Delete Avatars and all assets in this group? Their asset URIs will no longer work.'
  )
  expect(deleted).toBe(false)
  await user.click(within(dialog).getByRole('button', { name: 'Cancel' }))
  expect(deleted).toBe(false)
  await user.click(screen.getByRole('button', { name: 'Delete group' }))
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
    expect(
      await screen.findByRole('button', { name: 'Create asset group' })
    ).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Add asset' })).toBeDisabled()
    expect(
      await screen.findByRole('button', { name: 'Delete Avatar' })
    ).toBeDisabled()
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
