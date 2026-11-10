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

export type CurrencyWallet = {
  currency: 'CNY'
  quota: number
  used_quota: number
  quota_per_unit: number
}

export async function getCurrencyWallet(
  userId?: number
): Promise<CurrencyWallet> {
  const response = await api.get(`/api/user/${userId ?? 'self'}/currency/CNY`)
  return requireServerSuccess(response.data).data
}

export async function adjustCurrencyWallet(
  userId: number,
  adjustment: {
    delta_quota: number
    operation_id: string
    reason: string
  }
): Promise<CurrencyWallet> {
  const response = await api.post(
    `/api/user/${userId}/currency/CNY`,
    adjustment
  )
  return requireServerSuccess(response.data).data
}
