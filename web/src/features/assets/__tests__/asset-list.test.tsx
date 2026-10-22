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
import { render, screen } from '@testing-library/react'
import type { ReactNode } from 'react'
import { afterEach, describe, expect, test, vi } from 'vitest'

import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth-store'

import { Assets } from '..'
import { AssetProvidersDialog } from '../components/asset-providers-dialog'
import { AssetsTable } from '../components/assets-table'
import type { Asset } from '../types'

function renderList(component: ReactNode) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: 0 } },
  })
  return render(
    <QueryClientProvider client={client}>{component}</QueryClientProvider>
  )
}

describe('asset list states', () => {
  afterEach(() => useAuthStore.getState().auth.reset())

  test('disabled storage prevents uploads while the catalog remains available', async () => {
    useAuthStore.getState().auth.setUser({ id: 1, username: 'user', role: 1 })
    vi.spyOn(api, 'get').mockImplementation(async (url) => ({
      data: {
        success: true,
        data:
          url === '/api/status'
            ? { asset_library_enabled: false }
            : { items: [], total: 0, page: 1, page_size: 20 },
      },
    }))
    renderList(<Assets />)
    expect(await screen.findByText('No assets yet')).toBeVisible()
    expect(screen.getByRole('button', { name: 'Upload asset' })).toBeDisabled()
  })

  test('empty catalog explains how to add reusable assets', async () => {
    vi.spyOn(api, 'get').mockResolvedValue({
      data: {
        success: true,
        data: { items: [], total: 0, page: 1, page_size: 20 },
      },
    })
    renderList(<AssetsTable />)
    expect(await screen.findByText('No assets yet')).toBeVisible()
  })

  test('pending cleanup stays visible while rename and deletion are unavailable', async () => {
    vi.spyOn(api, 'get').mockResolvedValue({
      data: {
        success: true,
        data: {
          items: [
            {
              id: 'na_asset_deleted',
              name: 'Deleted reference',
              kind: 'image',
              size: 1024,
              status: 'deleted',
              error: 'Asset cleanup is pending',
            },
          ],
          total: 1,
          page: 1,
          page_size: 20,
        },
      },
    })
    renderList(<AssetsTable />)
    expect(await screen.findByText('Pending deletion')).toBeVisible()
    expect(screen.getByText('Asset cleanup is pending')).toBeVisible()
    expect(screen.getByRole('button', { name: 'Rename' })).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Delete' })).toBeDisabled()
    expect(
      screen.queryByRole('button', { name: 'Copy asset reference' })
    ).not.toBeInTheDocument()
  })

  test('catalog request failure offers a retry instead of an empty success state', async () => {
    vi.spyOn(api, 'get').mockRejectedValue(new Error('Unavailable'))
    renderList(<AssetsTable />)
    expect(await screen.findByRole('button', { name: 'Retry' })).toBeVisible()
    expect(screen.queryByText('No assets yet')).not.toBeInTheDocument()
  })

  test('provider business failure renders an error instead of hiding it as no replicas', async () => {
    vi.spyOn(api, 'get').mockResolvedValue({
      data: { success: false, message: 'Asset not found' },
    })
    const asset: Asset = {
      id: 'na_asset_example',
      name: 'Reference',
      kind: 'image',
      mime_type: 'image/png',
      size: 1,
      status: 'ready',
      created_at: 1,
    }
    renderList(<AssetProvidersDialog asset={asset} onClose={vi.fn()} />)
    expect(await screen.findByRole('button', { name: 'Retry' })).toBeVisible()
    expect(screen.queryByText('No provider replicas')).not.toBeInTheDocument()
  })

  test('provider replicas identify their channel and show safe failure feedback', async () => {
    vi.spyOn(api, 'get').mockResolvedValue({
      data: {
        success: true,
        data: {
          items: [
            {
              channel_id: 12,
              name: 'Video channel',
              status: 'failed',
              error: 'Asset provider rejected the asset',
            },
          ],
        },
      },
    })
    renderList(
      <AssetProvidersDialog
        asset={{
          id: 'na_asset_example',
          name: 'Reference',
          kind: 'image',
          mime_type: 'image/png',
          size: 1,
          status: 'ready',
          created_at: 1,
        }}
        onClose={vi.fn()}
      />
    )
    expect(await screen.findByText('Video channel')).toBeVisible()
    expect(screen.getByText('Asset provider rejected the asset')).toBeVisible()
  })
})
