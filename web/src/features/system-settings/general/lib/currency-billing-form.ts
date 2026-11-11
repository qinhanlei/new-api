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

import type { CurrencyBillingPolicy } from '../currency-billing-api'

export function createCurrencyBillingSchema(t: (key: string) => string) {
  const decimalPattern = /^[0-9]{1,8}(\.[0-9]{1,12})?$/
  const changeLimit = z
    .string()
    .refine(
      (value) =>
        decimalPattern.test(value) && Number(value) > 0 && Number(value) <= 50,
      t('Exchange rate change limits must be between 0 and 50 percent')
    )
  const intervalError = t(
    'Invalid exchange rate update interval or maximum age'
  )
  return z
    .object({
      cross_currency_enabled: z.boolean(),
      dynamic_enabled: z.boolean(),
      static_rate: z
        .string()
        .refine(
          (value) =>
            decimalPattern.test(value) &&
            Number(value) >= 0.01 &&
            Number(value) <= 1000,
          t('Exchange rate must be between 0.01 and 1000')
        ),
      interval_hours: z.coerce
        .number()
        .int(intervalError)
        .min(1, intervalError)
        .max(168, intervalError),
      max_quote_age_hours: z.coerce
        .number()
        .int(intervalError)
        .min(1, intervalError)
        .max(720, intervalError),
      max_change_percent: changeLimit,
      max_week_change_percent: changeLimit,
    })
    .superRefine((data, context) => {
      if (data.max_quote_age_hours < data.interval_hours) {
        context.addIssue({
          code: 'custom',
          path: ['max_quote_age_hours'],
          message: intervalError,
        })
      }
    })
}

export type CurrencyBillingFormValues = z.infer<
  ReturnType<typeof createCurrencyBillingSchema>
>

export function getCurrencyBillingFormValues(
  policy: CurrencyBillingPolicy
): CurrencyBillingFormValues {
  return {
    cross_currency_enabled: policy.cross_currency_enabled,
    dynamic_enabled: policy.dynamic_enabled,
    static_rate: policy.static_rate,
    interval_hours: policy.interval_hours,
    max_quote_age_hours: policy.max_quote_age_hours,
    max_change_percent: policy.max_change_percent,
    max_week_change_percent: policy.max_week_change_percent,
  }
}
