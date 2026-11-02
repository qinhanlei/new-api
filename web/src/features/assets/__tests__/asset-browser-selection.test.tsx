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
import { afterEach, expect, test, vi } from 'vitest'

import { api } from '@/lib/api'

import { assetQueryKeys } from '../api'
import { AssetBrowser } from '../components/asset-browser'
import type { AssetGroup, AssetValidationSummary } from '../types'

const first: AssetGroup = {
  Id: 'person-first',
  Name: 'First person',
  Description: '',
  ProjectName: 'default',
  GroupType: 'LivenessFace',
  Status: 'active',
}
const selected: AssetGroup = {
  ...first,
  Id: 'person-selected',
  Name: 'Selected person',
}
let client: QueryClient

afterEach(() => client?.clear())

test('a selected person removed during refresh shows an error instead of another person', async () => {
  const user = userEvent.setup()
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  let removed = false
  vi.spyOn(api, 'get').mockResolvedValue({
    data: {
      success: true,
      data: {
        Items: [],
        TotalCount: 0,
        PendingCount: 0,
        Page: 1,
        PageSize: 1,
      },
    },
  })
  const post = vi
    .spyOn(api, 'post')
    .mockImplementation(async (_url, _body, config) => {
      const action = (config?.params as { Action?: string } | undefined)?.Action
      if (action === 'ListAssetGroups') {
        return {
          data: {
            success: true,
            data: { Items: removed ? [first] : [first, selected] },
          },
        }
      }
      if (action === 'GetAssetGroup') {
        throw new Error('The selected person is unavailable')
      }
      return { data: { success: true, data: { Items: [], TotalCount: 0 } } }
    })
  render(
    <QueryClientProvider client={client}>
      <AssetBrowser project='default' scope={{}} admin={false} />
    </QueryClientProvider>
  )
  await screen.findByRole('option', { name: first.Name })
  await user.selectOptions(
    screen.getByRole('combobox', { name: 'Library type' }),
    'LivenessFace'
  )
  expect(screen.getByRole('combobox', { name: 'Library type' })).toHaveValue(
    'LivenessFace'
  )
  await screen.findByRole('option', { name: selected.Name })
  await user.selectOptions(
    screen.getByRole('combobox', { name: 'Asset group' }),
    selected.Id
  )
  removed = true
  await act(async () => {
    await client.invalidateQueries({ queryKey: assetQueryKeys.all })
  })
  expect(
    await screen.findByText('The selected person is unavailable')
  ).toBeVisible()
  expect(screen.getByRole('combobox', { name: 'Asset group' })).not.toHaveValue(
    first.Id
  )
  await waitFor(() =>
    expect(post).toHaveBeenCalledWith(
      '/api/assets/actions',
      expect.objectContaining({ Id: selected.Id }),
      expect.objectContaining({
        params: expect.objectContaining({ Action: 'GetAssetGroup' }),
      })
    )
  )
})

test.each([false, true])(
  'viewing a completed record outside the loaded page uses its exact group ID (unavailable=%s)',
  async (unavailable) => {
    const user = userEvent.setup()
    client = new QueryClient({
      defaultOptions: {
        queries: { retry: false },
        mutations: { retry: false },
      },
    })
    const record: AssetValidationSummary = {
      SessionId: 'validation-na-completed',
      Name: 'Verified second person',
      Status: 'created',
      GroupId: selected.Id,
      ChannelId: 7,
      ExpiresAt: 1,
      CanPoll: false,
      CanContinue: false,
      NeedsReview: false,
      BindingPending: false,
      CanArchive: true,
      ArchivedAt: 0,
      CreatedAt: 1000,
      LastCheckedAt: 1200,
    }
    vi.spyOn(api, 'get').mockResolvedValue({
      data: {
        success: true,
        data: {
          Items: [record],
          TotalCount: 1,
          PendingCount: 0,
          Page: 1,
          PageSize: 20,
        },
      },
    })
    const post = vi
      .spyOn(api, 'post')
      .mockImplementation(async (_url, _body, config) => {
        const action = (config?.params as { Action?: string } | undefined)
          ?.Action
        if (action === 'ListAssetGroups') {
          return {
            data: {
              success: true,
              data: { Items: [first], NextToken: 'another-page' },
            },
          }
        }
        if (action === 'GetAssetGroup') {
          if (unavailable) throw new Error('The verified person was deleted')
          return { data: { success: true, data: selected } }
        }
        return { data: { success: true, data: { Items: [], TotalCount: 0 } } }
      })
    render(
      <QueryClientProvider client={client}>
        <AssetBrowser project='default' scope={{}} admin={false} />
      </QueryClientProvider>
    )
    await screen.findByRole('option', { name: first.Name })
    await user.selectOptions(
      screen.getByRole('combobox', { name: 'Library type' }),
      'LivenessFace'
    )
    expect(screen.getByRole('combobox', { name: 'Library type' })).toHaveValue(
      'LivenessFace'
    )
    await user.click(
      await screen.findByRole('button', { name: 'Verification records' })
    )
    await user.click(await screen.findByRole('button', { name: 'View group' }))
    if (unavailable) {
      expect(
        await screen.findByText('The verified person was deleted')
      ).toBeVisible()
      expect(
        screen.getByRole('dialog', { name: 'Verification records' })
      ).toBeVisible()
    } else {
      await waitFor(() =>
        expect(
          screen.queryByRole('dialog', { name: 'Verification records' })
        ).not.toBeInTheDocument()
      )
      expect(screen.getByRole('combobox', { name: 'Asset group' })).toHaveValue(
        selected.Id
      )
      expect(screen.getByRole('option', { name: selected.Name })).toBeVisible()
      await user.click(screen.getByRole('button', { name: 'Add asset' }))
      const upload = await screen.findByRole('dialog', { name: 'Add asset' })
      expect(within(upload).getByText(selected.Name)).toBeVisible()
      expect(within(upload).getByLabelText('Asset URL')).toBeVisible()
    }
    expect(post).toHaveBeenCalledWith(
      '/api/assets/actions',
      { ProjectName: 'default', Id: selected.Id },
      expect.objectContaining({
        params: expect.objectContaining({ Action: 'GetAssetGroup' }),
      })
    )
    expect(
      post.mock.calls.some(
        (call) =>
          (call[2]?.params as { Action?: string })?.Action ===
          'GetVisualValidateResult'
      )
    ).toBe(false)
  }
)
