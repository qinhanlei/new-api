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
import { z } from 'zod'

import type { BillingRateRule, BillingRateSource } from './types'

export const billingRateInputSchema = z
  .string()
  .trim()
  .refine(
    (value) =>
      value === '' ||
      (/^\d+(?:\.\d{1,6})?$/.test(value) && Number(value) <= 100),
    'Enter a rate from 0 to 100 with up to six decimal places, or leave blank to inherit.'
  )

export function billingRateSourceKey(source: BillingRateSource): string {
  switch (source) {
    case 'user':
      return 'User model rate'
    case 'model':
      return 'Global model rate'
    case 'group_special':
      return 'Special group rate'
    case 'group':
      return 'Group Ratio'
  }
}

export function normalizeBillingRate(value: string): string | null {
  const parsed = billingRateInputSchema.parse(value)
  return parsed === '' ? null : String(Number(parsed))
}

export function billingRateChanges(
  rules: BillingRateRule[],
  drafts: Record<string, string>
) {
  const existing = new Map(rules.map((rule) => [rule.model_name, rule]))
  return Object.entries(drafts).flatMap(([model_name, value]) => {
    const rate = normalizeBillingRate(value)
    const rule = existing.get(model_name)
    if (rate === (rule?.rate == null ? null : String(Number(rule.rate)))) {
      return []
    }
    return [{ model_name, rate, expected_version: rule?.version ?? 0 }]
  })
}

export const billingRatesFormSchema = z.object({
  entries: z.array(
    z.object({
      model_name: z.string().trim().min(1),
      rate: billingRateInputSchema,
    })
  ),
})
export type BillingRatesFormValues = z.infer<typeof billingRatesFormSchema>
