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
export type AssetType = 'Image' | 'Video' | 'Audio'

export interface AssetScope {
  userId?: number
  allUsers?: boolean
}

export interface AssetGroup {
  Id: string
  Name: string
  Description: string
  ProjectName: string
  GroupType: string
  UserId?: number
}

export interface Asset {
  Id: string
  Name: string
  GroupId: string
  ProjectName: string
  AssetType: AssetType
  Status: string
  URL: string
  Error?: { Code: string; Message: string }
  UserId?: number
}

export interface AssetList<T> {
  Items: T[]
  TotalCount: number
}

export interface AssetReplica {
  channel_id: number
  channel_name: string
  status: string
  error_code: string
  error_message: string
  last_attempt_at: number
  can_retry: boolean
}

export interface AssetStorageConfig {
  provider?: string
  enabled: boolean
  endpoint: string
  region: string
  bucket: string
  access_key: string
  force_path_style: boolean
  revision: number
  has_secret: boolean
}

export type AssetStorageUpdate = Omit<AssetStorageConfig, 'has_secret'> & {
  secret_key?: string
}

export interface AssetChannelConfig {
  channel_id: number
  enabled: boolean
  endpoint: string
  project_name: string
  auth_type: 'bearer' | 'aksk'
  access_key: string
  has_secret: boolean
  revision: number
}

export type AssetChannelUpdate = Omit<
  AssetChannelConfig,
  'has_secret' | 'channel_id'
> & { secret_key?: string }
