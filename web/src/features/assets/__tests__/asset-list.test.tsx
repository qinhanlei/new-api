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
import { act, fireEvent, render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'

import { api } from '@/lib/api'

import { AssetListTable } from '../components/asset-list-table'
import type { Asset, AssetGroup } from '../types'

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

function renderList() {
  return render(
    <QueryClientProvider client={client}>
      <AssetListTable group={group} scope={{ userId: 23 }} admin={false} />
    </QueryClientProvider>
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

afterEach(() => client.clear())

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
