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
import type { ReactNode } from 'react'
import { toast } from 'sonner'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'

import { SectionPageLayout } from '@/components/layout'
import { api } from '@/lib/api'

import { AssetListTable } from '../components/asset-list-table'
import type { Asset, AssetGroup, AssetScope } from '../types'

const group: AssetGroup = {
  Id: 'group-na-portraits',
  Name: 'Portraits',
  Description: '',
  ProjectName: 'studio',
  GroupType: 'AIGC',
}
const portrait: Asset = {
  Id: 'asset-na-portrait',
  Name: 'Portrait',
  GroupId: group.Id,
  ProjectName: group.ProjectName,
  AssetType: 'Image',
  Status: 'Active',
  URL: 'https://storage.example/portrait.png?signature=first',
}
let client: QueryClient
let items: Asset[]

function ListTestWrapper({ children }: { children: ReactNode }) {
  return (
    <QueryClientProvider client={client}>
      <SectionPageLayout fixedContent>
        <SectionPageLayout.Content>{children}</SectionPageLayout.Content>
      </SectionPageLayout>
    </QueryClientProvider>
  )
}

function renderList(scope: AssetScope = { userId: 23 }, selectedGroup = group) {
  return render(
    <AssetListTable group={selectedGroup} scope={scope} admin={false} />,
    { wrapper: ListTestWrapper }
  )
}

beforeEach(() => {
  client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  items = [{ ...portrait }]
  vi.spyOn(api, 'post').mockImplementation(async () => ({
    data: { success: true, data: { Items: items, TotalCount: items.length } },
  }))
})

afterEach(() => {
  client.clear()
  vi.unstubAllGlobals()
})

test('asset links and local creation times have separate columns and copy the full URI', async () => {
  const user = userEvent.setup()
  const clipboard = vi.spyOn(navigator.clipboard, 'writeText')
  items = [
    {
      ...portrait,
      CreateTime: new Date(2026, 9, 29, 12, 34, 56).toISOString(),
    },
  ]
  renderList()
  const row = await screen.findByRole('row', { name: /Portrait/ })
  expect(screen.getByRole('columnheader', { name: 'Asset URI' })).toBeVisible()
  expect(screen.getByRole('columnheader', { name: 'Created At' })).toBeVisible()
  expect(
    within(row)
      .getAllByRole('cell', { name: 'Portrait' })
      .find((cell) => within(cell).queryByText('Portrait'))
  ).not.toHaveTextContent('asset://')
  expect(within(row).getByText('2026-10-29 12:34:56')).toBeVisible()
  await user.click(within(row).getByRole('button', { name: 'Copy asset URI' }))
  expect(clipboard).toHaveBeenCalledWith('asset://asset-na-portrait')
})

test('loaded thumbnails retain their URL through polling and open the current full preview', async () => {
  const user = userEvent.setup()
  renderList()
  const thumbnail = await screen.findByRole('img', { name: 'Portrait' })
  fireEvent.load(thumbnail)
  items = [
    {
      ...portrait,
      URL: 'https://storage.example/portrait.png?signature=renewed',
    },
  ]
  await act(() => client.invalidateQueries({ queryKey: ['assets'] }))
  expect(screen.getByRole('img', { name: 'Portrait' })).toHaveAttribute(
    'src',
    portrait.URL
  )
  expect(screen.getByRole('img', { name: 'Portrait' })).toHaveAttribute(
    'loading',
    'lazy'
  )
  await user.click(screen.getByRole('button', { name: 'Preview Portrait' }))
  expect(
    within(screen.getByRole('dialog')).getByRole('img', { name: 'Portrait' })
  ).toHaveAttribute('src', items[0].URL)
})

test('failed thumbnails retry one refreshed URL and then remain a stable fallback', async () => {
  renderList()
  fireEvent.error(await screen.findByRole('img', { name: 'Portrait' }))
  expect(screen.getByTitle('Image not available')).toBeVisible()
  items = [
    {
      ...portrait,
      URL: 'https://storage.example/portrait.png?signature=second',
    },
  ]
  await act(() => client.invalidateQueries({ queryKey: ['assets'] }))
  const retry = await screen.findByRole('img', { name: 'Portrait' })
  expect(retry).toHaveAttribute('src', items[0].URL)
  fireEvent.error(retry)
  items = [
    {
      ...portrait,
      URL: 'https://storage.example/portrait.png?signature=third',
    },
  ]
  await act(() => client.invalidateQueries({ queryKey: ['assets'] }))
  expect(
    screen.queryByRole('img', { name: 'Portrait' })
  ).not.toBeInTheDocument()
  expect(screen.getByTitle('Image not available')).toBeVisible()
})

test('missing images and non-image assets have placeholders and invalid dates stay empty', async () => {
  items = [
    { ...portrait, URL: '', CreateTime: 'invalid' },
    { ...portrait, Id: 'asset-na-film', Name: 'Film', AssetType: 'Video' },
    { ...portrait, Id: 'asset-na-music', Name: 'Music', AssetType: 'Audio' },
  ]
  renderList()
  expect(
    await screen.findByRole('button', { name: 'Preview Portrait' })
  ).toBeDisabled()
  expect(screen.getByRole('button', { name: 'Preview Film' })).toBeEnabled()
  expect(screen.getByRole('button', { name: 'Preview Music' })).toBeEnabled()
  expect(screen.queryByRole('img')).not.toBeInTheDocument()
  const row = screen.getByRole('row', { name: /Portrait/ })
  expect(within(row).getByText('—')).toBeVisible()
  expect(screen.queryByText('Invalid Date')).not.toBeInTheDocument()
  items = [{ ...portrait }]
  await act(() => client.invalidateQueries({ queryKey: ['assets'] }))
  expect(await screen.findByRole('img', { name: 'Portrait' })).toHaveAttribute(
    'src',
    portrait.URL
  )
})

test('page selection survives refresh by ID and clears when filtering or changing pages', async () => {
  const user = userEvent.setup()
  items = [portrait, { ...portrait, Id: 'asset-na-two', Name: 'Second' }]
  let filtered = false
  vi.mocked(api.post).mockImplementation(async (_url, body) => {
    const request = body as { PageNumber: number; Filter: { Name: string } }
    filtered = Boolean(request.Filter.Name)
    return {
      data: {
        success: true,
        data: { Items: items, TotalCount: filtered ? items.length : 21 },
      },
    }
  })
  renderList()
  const selectAll = await screen.findByRole('checkbox', { name: 'Select all' })
  await user.click(
    await screen.findByRole('checkbox', { name: 'Select row 1' })
  )
  expect(selectAll).toBePartiallyChecked()
  await user.click(selectAll)
  expect(
    screen.getByRole('toolbar', { name: 'Bulk actions for 2 selected records' })
  ).toBeVisible()
  items = [{ ...items[1], Status: 'Processing' }]
  await act(() => client.invalidateQueries({ queryKey: ['assets'] }))
  expect(screen.getByRole('checkbox', { name: 'Select row 1' })).toBeChecked()
  expect(
    await screen.findByRole('toolbar', {
      name: 'Bulk actions for 1 selected records',
    })
  ).toBeVisible()
  await user.click(screen.getByRole('button', { name: 'Go to next page' }))
  expect(screen.queryByRole('toolbar')).not.toBeInTheDocument()
  await user.click(
    await screen.findByRole('checkbox', { name: 'Select row 1' })
  )
  await user.type(screen.getByPlaceholderText('Filter by name...'), 'Second')
  await waitFor(() => expect(filtered).toBe(true))
  expect(screen.queryByRole('toolbar')).not.toBeInTheDocument()
  expect(
    screen.getByRole('checkbox', { name: 'Select row 1' })
  ).not.toBeChecked()
})

test('batch deletion reports partial outcomes and retries only failed assets with the original owner', async () => {
  const user = userEvent.setup()
  const success = vi.spyOn(toast, 'success').mockImplementation(() => 'toast')
  items = [
    portrait,
    { ...portrait, Id: 'asset-na-two', Name: 'Second' },
    { ...portrait, Id: 'asset-na-three', Name: 'Third' },
  ]
  let attempts = 0
  vi.mocked(api.post).mockImplementation(async (url) => {
    if (url === '/api/assets/batch-delete') {
      attempts++
      items = attempts === 1 ? [items[2]] : []
      const results =
        attempts === 1
          ? [
              { Id: portrait.Id, Status: 'deleted' },
              { Id: 'asset-na-two', Status: 'not_found' },
              {
                Id: 'asset-na-three',
                Status: 'failed',
                Error: {
                  Code: 'AssetGroupUnavailable',
                  Message: 'The material group is being deleted',
                },
              },
            ]
          : [{ Id: 'asset-na-three', Status: 'deleted' }]
      return { data: { success: true, data: { Items: results } } }
    }
    return {
      data: { success: true, data: { Items: items, TotalCount: items.length } },
    }
  })
  renderList()
  await user.click(await screen.findByRole('checkbox', { name: 'Select all' }))
  await user.click(
    screen.getByRole('button', { name: 'Delete selected assets' })
  )
  const dialog = screen.getByRole('alertdialog')
  expect(dialog).toHaveTextContent(
    'Delete 3 selected assets? Their asset URIs will no longer work.'
  )
  await user.click(within(dialog).getByRole('button', { name: 'Delete' }))
  expect(await within(dialog).findByRole('status')).toHaveTextContent(
    'Deleted: 1. Not found: 1. Failed: 1.'
  )
  expect(dialog).toHaveTextContent(
    'Third: AssetGroupUnavailable: The material group is being deleted'
  )
  expect(success).not.toHaveBeenCalled()
  await user.click(within(dialog).getByRole('button', { name: 'Retry' }))
  await waitFor(() =>
    expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
  )
  const calls = vi
    .mocked(api.post)
    .mock.calls.filter(([url]) => url === '/api/assets/batch-delete')
  expect(calls.map(([, body]) => body)).toEqual([
    {
      ProjectName: 'studio',
      Ids: [portrait.Id, 'asset-na-two', 'asset-na-three'],
    },
    { ProjectName: 'studio', Ids: ['asset-na-three'] },
  ])
  for (const [, , options] of calls) {
    expect(options).toMatchObject({ params: { user_id: 23 } })
  }
  expect(success).toHaveBeenCalledWith('Deleted: 2. Not found: 1.')
  expect(await screen.findByText('No assets')).toBeVisible()
  await waitFor(() =>
    expect(
      screen.getByRole('region', { name: 'Asset library' })
    ).toContainElement(document.activeElement as HTMLElement)
  )
})

test('cancelling batch deletion restores focus and keeps the selection', async () => {
  const user = userEvent.setup()
  renderList()
  await user.click(
    await screen.findByRole('checkbox', { name: 'Select row 1' })
  )
  const trigger = screen.getByRole('button', { name: 'Delete selected assets' })
  await user.click(trigger)
  await user.click(
    within(screen.getByRole('alertdialog')).getByRole('button', {
      name: 'Cancel',
    })
  )
  await waitFor(() => expect(trigger).toHaveFocus())
  expect(screen.getByRole('checkbox', { name: 'Select row 1' })).toBeChecked()
  expect(api.post).not.toHaveBeenCalledWith(
    '/api/assets/batch-delete',
    expect.anything(),
    expect.anything()
  )
})

test('a pending batch cannot be submitted twice or closed and a lost response stays unconfirmed', async () => {
  const user = userEvent.setup()
  const success = vi.spyOn(toast, 'success').mockImplementation(() => 'toast')
  let rejectRequest: (error: Error) => void = () => undefined
  const pending = new Promise<never>((_resolve, reject) => {
    rejectRequest = reject
  })
  vi.mocked(api.post).mockImplementation(async (url) => {
    if (url === '/api/assets/batch-delete') return pending
    return {
      data: { success: true, data: { Items: items, TotalCount: items.length } },
    }
  })
  renderList()
  await user.click(
    await screen.findByRole('checkbox', { name: 'Select row 1' })
  )
  await user.click(
    screen.getByRole('button', { name: 'Delete selected assets' })
  )
  const dialog = screen.getByRole('alertdialog')
  await user.dblClick(within(dialog).getByRole('button', { name: 'Delete' }))
  expect(within(dialog).getByRole('button', { name: 'Delete' })).toBeDisabled()
  expect(within(dialog).getByRole('button', { name: 'Cancel' })).toBeDisabled()
  await user.keyboard('{Escape}')
  expect(dialog).toBeVisible()
  await act(async () => rejectRequest(new Error('Connection interrupted')))
  expect(await within(dialog).findByRole('alert')).toHaveTextContent(
    'Deletion was not confirmed. Check the refreshed list before retrying.'
  )
  expect(dialog).toHaveTextContent('Connection interrupted')
  expect(
    vi
      .mocked(api.post)
      .mock.calls.filter(([url]) => url === '/api/assets/batch-delete')
  ).toHaveLength(1)
  expect(success).not.toHaveBeenCalled()
})

test('a malformed successful response is unconfirmed instead of claiming deletion', async () => {
  const user = userEvent.setup()
  const success = vi.spyOn(toast, 'success').mockImplementation(() => 'toast')
  vi.mocked(api.post).mockImplementation(async (url) => ({
    data: {
      success: true,
      data: {
        Items: url === '/api/assets/batch-delete' ? [] : items,
        TotalCount: items.length,
      },
    },
  }))
  renderList()
  await user.click(
    await screen.findByRole('checkbox', { name: 'Select row 1' })
  )
  await user.click(
    screen.getByRole('button', { name: 'Delete selected assets' })
  )
  const dialog = screen.getByRole('alertdialog')
  await user.click(within(dialog).getByRole('button', { name: 'Delete' }))
  expect(await within(dialog).findByRole('alert')).toHaveTextContent(
    'The batch deletion result could not be verified.'
  )
  expect(success).not.toHaveBeenCalled()
})

test.each([{ allUsers: true }, { userId: 23 }])(
  'read-only scope or a frozen group cannot select or delete: %j',
  async (scope) => {
    renderList(
      scope,
      scope.allUsers ? group : { ...group, Status: 'delete_pending' }
    )
    expect(
      await screen.findByRole('button', { name: 'Delete Portrait' })
    ).toBeDisabled()
    expect(screen.queryByRole('checkbox')).not.toBeInTheDocument()
    expect(
      screen.queryByRole('button', { name: 'Delete selected assets' })
    ).not.toBeInTheDocument()
  }
)

test('a frozen group disables an already open batch confirmation', async () => {
  const user = userEvent.setup()
  const view = renderList()
  await user.click(
    await screen.findByRole('checkbox', { name: 'Select row 1' })
  )
  await user.click(
    screen.getByRole('button', { name: 'Delete selected assets' })
  )
  view.rerender(
    <AssetListTable
      group={{ ...group, Status: 'delete_pending' }}
      scope={{ userId: 23 }}
      admin={false}
    />
  )
  expect(
    within(screen.getByRole('alertdialog')).getByRole('button', {
      name: 'Delete',
    })
  ).toBeDisabled()
})

test('mobile list exposes page selection and the same batch confirmation', async () => {
  vi.spyOn(window, 'matchMedia').mockImplementation((query) => ({
    matches:
      query.includes('max-width') || query.includes('prefers-reduced-motion'),
    media: query,
    onchange: null,
    addListener: vi.fn(),
    removeListener: vi.fn(),
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
    dispatchEvent: () => false,
  }))
  const user = userEvent.setup()
  renderList()
  await user.click(
    await screen.findByRole('checkbox', { name: 'Select row 1' })
  )
  expect(screen.queryByRole('table')).not.toBeInTheDocument()
  await user.click(
    screen.getByRole('button', { name: 'Delete selected assets' })
  )
  expect(screen.getByRole('alertdialog')).toHaveTextContent(
    'Delete 1 selected assets?'
  )
})

test('changing the viewed owner and group cannot retarget an open batch confirmation', async () => {
  const user = userEvent.setup()
  vi.mocked(api.post).mockImplementation(async (url) => ({
    data: {
      success: true,
      data: {
        Items:
          url === '/api/assets/batch-delete'
            ? [{ Id: portrait.Id, Status: 'deleted' }]
            : items,
        TotalCount: items.length,
      },
    },
  }))
  const view = renderList()
  await user.click(
    await screen.findByRole('checkbox', { name: 'Select row 1' })
  )
  await user.click(
    screen.getByRole('button', { name: 'Delete selected assets' })
  )
  view.rerender(
    <AssetListTable
      group={{ ...group, Id: 'group-na-other', ProjectName: 'other' }}
      scope={{ userId: 99 }}
      admin={false}
    />
  )
  await user.click(
    within(screen.getByRole('alertdialog')).getByRole('button', {
      name: 'Delete',
    })
  )
  expect(api.post).toHaveBeenCalledWith(
    '/api/assets/batch-delete',
    { ProjectName: 'studio', Ids: [portrait.Id] },
    expect.objectContaining({
      params: expect.objectContaining({ user_id: 23 }),
    })
  )
})

test('deleting the last page returns to the remaining page without losing the name filter', async () => {
  const user = userEvent.setup()
  let deleted = false
  const firstPage = Array.from({ length: 20 }, (_, index) => ({
    ...portrait,
    Id: `asset-na-first-${index}`,
    Name: `Portrait ${index + 1}`,
  }))
  vi.mocked(api.post).mockImplementation(async (url, body) => {
    if (url === '/api/assets/batch-delete') {
      deleted = true
      return {
        data: {
          success: true,
          data: { Items: [{ Id: portrait.Id, Status: 'deleted' }] },
        },
      }
    }
    const request = body as { PageNumber: number }
    let pageItems = firstPage
    if (request.PageNumber !== 1) pageItems = deleted ? [] : [portrait]
    return {
      data: {
        success: true,
        data: { Items: pageItems, TotalCount: deleted ? 20 : 21 },
      },
    }
  })
  renderList()
  await screen.findByText('Portrait 1')
  await user.type(screen.getByPlaceholderText('Filter by name...'), 'Portrait')
  await waitFor(() =>
    expect(api.post).toHaveBeenCalledWith(
      '/api/assets/actions',
      expect.objectContaining({
        Filter: expect.objectContaining({ Name: 'Portrait' }),
      }),
      expect.anything()
    )
  )
  await user.click(screen.getByRole('button', { name: 'Go to next page' }))
  await screen.findByText('Portrait')
  await user.click(screen.getByRole('checkbox', { name: 'Select row 1' }))
  await user.click(
    screen.getByRole('button', { name: 'Delete selected assets' })
  )
  await user.click(
    within(screen.getByRole('alertdialog')).getByRole('button', {
      name: 'Delete',
    })
  )
  expect(await screen.findByText('Portrait 1')).toBeVisible()
  expect(screen.getByRole('button', { name: 'Go to next page' })).toBeDisabled()
  expect(screen.getByPlaceholderText('Filter by name...')).toHaveValue(
    'Portrait'
  )
})
