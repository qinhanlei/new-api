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
export type Asset = {
  id: string
  name: string
  kind: 'image' | 'video' | 'audio'
  mime_type: string
  size: number
  status: string
  error?: string
  width?: number
  height?: number
  duration_seconds?: number
  created_at: number
}

export type AssetProvider = {
  channel_id: number
  name: string
  status: string
  error?: string
}

export type AssetPage = {
  items: Asset[]
  total: number
  page: number
  page_size: number
}
