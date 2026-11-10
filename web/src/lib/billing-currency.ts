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
import type { BillingCurrency } from './currency'

/** Currency belongs to the stored expression, including its request rules. */
export function splitBillingCurrency(source: string) {
  const match = source.match(/^\s*(v1:|v2:(USD|CNY):)/)
  return {
    prefix: match?.[0] ?? '',
    body: match ? source.slice(match[0].length) : source,
    currency: (match?.[2] ?? 'USD') as BillingCurrency,
    version: match?.[2] ? (2 as const) : (1 as const),
  }
}

export function billingExpressionCurrency(source?: string): BillingCurrency {
  return splitBillingCurrency(source ?? '').currency
}

export function preserveBillingCurrency(
  original: string,
  body: string
): string {
  return splitBillingCurrency(original).prefix + splitBillingCurrency(body).body
}
