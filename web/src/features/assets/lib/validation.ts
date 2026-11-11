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
import { convertDetectedLanguage } from '@/i18n/languages'
import { getServerErrorSources } from '@/lib/server-error-message'

// The provider supports three H5 languages. Other console languages use English.
export function assetValidationH5Link(
  raw: string | undefined,
  language: string
): string {
  if (!raw) return ''
  try {
    const url = new URL(raw)
    if (url.protocol !== 'https:' || url.username || url.password) return ''
    const normalized = convertDetectedLanguage(language)
    let lng = 'en'
    if (normalized === 'zhCN') lng = 'zh'
    if (normalized === 'zhTW') lng = 'zh-Hant'
    url.searchParams.set('lng', lng)
    return url.href
  } catch {
    return ''
  }
}

// Keep the server's cooldown when a transport failure bypasses the session view.
export function assetValidationRetryAt(error: unknown, now: number): number {
  for (const source of getServerErrorSources(error)) {
    const response = source.response as
      | { headers?: Record<string, unknown> }
      | undefined
    const headers = response?.headers
    const raw = headers?.['retry-after'] ?? headers?.['Retry-After']
    if (typeof raw !== 'string' && typeof raw !== 'number') continue
    const value = String(raw).trim()
    let timestamp = 0
    if (/^\d+$/.test(value)) {
      timestamp = now + Number(value) * 1000
    } else if (/^[a-z]/i.test(value)) {
      timestamp = Date.parse(value)
    }
    if (Number.isFinite(timestamp) && timestamp > now && timestamp <= 8.64e15) {
      return timestamp
    }
  }
  return 0
}
