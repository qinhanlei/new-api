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

export type CurrencyBillingPolicy = {
  cross_currency_enabled: boolean
  dynamic_enabled: boolean
  static_rate: string
  interval_hours: number
  max_quote_age_hours: number
  max_change_percent: string
  max_week_change_percent: string
  version: number
  active_rate_id: number
  updated_at: number
}

export type ExchangeRateObservation = {
  id: number
  rate: string
  source: string
  source_time: number
  observed_at: number
  policy_version: number
  accepted: boolean
  reason: string
}

export type CurrencyBillingSettings = {
  policy: CurrencyBillingPolicy
  rate: {
    enabled: boolean
    rate_id?: number
    rate?: string
    source?: string
    source_time?: number
    policy_version: number
  }
  latest_observation: ExchangeRateObservation
  status: 'ready' | 'last_good' | 'unavailable'
}

export async function getCurrencyBillingSettings(): Promise<CurrencyBillingSettings> {
  const response = await api.get('/api/currency-billing')
  return requireServerSuccess(response.data).data
}

export async function previewCurrencyBillingRate(): Promise<ExchangeRateObservation> {
  const response = await api.get('/api/currency-billing/preview')
  return requireServerSuccess(response.data).data
}

export async function updateCurrencyBillingSettings(request: {
  policy: CurrencyBillingPolicy
  baseline_rate?: string
}): Promise<CurrencyBillingSettings> {
  const response = await api.put('/api/currency-billing', request)
  return requireServerSuccess(response.data).data
}
