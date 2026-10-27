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
import { authRequestOptions } from '@/lib/secure-verification'
import { requireServerSuccess } from '@/lib/server-error-message'

import type {
  AssetChannelConfig,
  AssetChannelUpdate,
  AssetReplica,
  AssetScope,
  AssetStorageConfig,
  AssetStorageUpdate,
} from './types'

interface AssetResponse<T> {
  success: boolean
  data: T
  message?: string
}

export const assetQueryKeys = {
  all: ['assets'] as const,
  storage: ['asset-storage'] as const,
  channel: (id: number) => ['channel-assets', id] as const,
}

export async function getAssetConfig(signal?: AbortSignal) {
  const { data } = await api.get<AssetResponse<{ enabled: boolean }>>(
    '/api/assets/config',
    { ...authRequestOptions, signal }
  )
  return requireServerSuccess(data).data
}

export async function executeAssetAction<T>(
  action: string,
  body: Record<string, unknown>,
  scope: AssetScope = {},
  signal?: AbortSignal
) {
  const { data } = await api.post<AssetResponse<T>>(
    '/api/assets/actions',
    body,
    {
      ...authRequestOptions,
      signal,
      params: {
        Action: action,
        Version: '2024-01-01',
        user_id: scope.userId,
        all_users: scope.allUsers || undefined,
      },
    }
  )
  return requireServerSuccess(data).data
}

export async function uploadAsset(body: FormData, scope: AssetScope) {
  const { data } = await api.post<AssetResponse<{ Id: string }>>(
    '/api/assets/upload',
    body,
    {
      ...authRequestOptions,
      params: { user_id: scope.userId },
      headers: { 'Content-Type': undefined },
    }
  )
  return requireServerSuccess(data).data
}

export async function getAssetReplicas(
  id: string,
  scope: AssetScope,
  signal?: AbortSignal
) {
  const { data } = await api.get<AssetResponse<AssetReplica[]>>(
    `/api/assets/${encodeURIComponent(id)}/replicas`,
    { ...authRequestOptions, signal, params: { user_id: scope.userId } }
  )
  return requireServerSuccess(data).data
}

export async function getAssetStorage(signal?: AbortSignal) {
  const { data } = await api.get<AssetResponse<AssetStorageConfig>>(
    '/api/option/asset_storage',
    { ...authRequestOptions, signal }
  )
  return requireServerSuccess(data).data
}

export async function saveAssetStorage(values: AssetStorageUpdate) {
  const { data } = await api.put<AssetResponse<AssetStorageConfig>>(
    '/api/option/asset_storage',
    values,
    authRequestOptions
  )
  return requireServerSuccess(data).data
}

export async function testAssetStorage(values: AssetStorageUpdate) {
  const { data } = await api.post<AssetResponse<unknown>>(
    '/api/option/asset_storage/test',
    values,
    authRequestOptions
  )
  requireServerSuccess(data)
}

export async function getAssetChannel(id: number, signal?: AbortSignal) {
  const { data } = await api.get<AssetResponse<AssetChannelConfig>>(
    `/api/channel/${id}/assets`,
    { ...authRequestOptions, signal }
  )
  return requireServerSuccess(data).data
}

export async function saveAssetChannel(id: number, values: AssetChannelUpdate) {
  const { data } = await api.put<AssetResponse<AssetChannelConfig>>(
    `/api/channel/${id}/assets`,
    values,
    authRequestOptions
  )
  return requireServerSuccess(data).data
}
