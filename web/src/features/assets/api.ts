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
import { api } from '@/lib/api'
import { requireServerSuccess } from '@/lib/server-error-message'

import type { Asset, AssetPage, AssetProvider } from './types'

type Response<T> = { success: boolean; message: string; data: T }

export async function listAssets(
  page: number,
  pageSize: number
): Promise<AssetPage> {
  const response = await api.get<Response<AssetPage>>('/api/assets', {
    params: { page, page_size: pageSize },
  })
  return requireServerSuccess(response.data).data
}

export async function uploadAsset(input: {
  name: string
  file?: File
  url?: string
}): Promise<Asset> {
  let body: FormData | { name: string; url?: string } = {
    name: input.name,
    url: input.url,
  }
  if (input.file) {
    body = new FormData()
    body.set('name', input.name)
    body.set('file', input.file)
  }
  const response = await api.post<Response<Asset>>('/api/assets', body)
  return requireServerSuccess(response.data).data
}

export async function renameAsset(id: string, name: string): Promise<Asset> {
  const response = await api.patch<Response<Asset>>(
    `/api/assets/${encodeURIComponent(id)}`,
    { name }
  )
  return requireServerSuccess(response.data).data
}

export async function deleteAsset(id: string): Promise<void> {
  const response = await api.delete<Response<unknown>>(
    `/api/assets/${encodeURIComponent(id)}`
  )
  requireServerSuccess(response.data)
}

export async function listAssetProviders(id: string): Promise<AssetProvider[]> {
  const response = await api.get<Response<{ items: AssetProvider[] }>>(
    `/api/assets/${encodeURIComponent(id)}/providers`
  )
  return requireServerSuccess(response.data).data.items
}
