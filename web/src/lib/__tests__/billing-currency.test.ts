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
import { afterEach, expect, test } from 'vitest'

import {
  billingAmountToQuota,
  billingQuotaToAmount,
  formatBillingQuota,
  formatCurrencyFromUSD,
} from '@/lib/currency'
import {
  DEFAULT_CURRENCY_CONFIG,
  useSystemConfigStore,
} from '@/stores/system-config-store'

afterEach(() => {
  useSystemConfigStore.getState().setConfig({
    currency: { ...DEFAULT_CURRENCY_CONFIG },
  })
})

test.each(['USD', 'CNY', 'CUSTOM', 'TOKENS'] as const)(
  'keeps billable USD and CNY amounts in their original currencies with %s display mode',
  (quotaDisplayType) => {
    useSystemConfigStore.getState().setConfig({
      currency: {
        ...DEFAULT_CURRENCY_CONFIG,
        quotaDisplayType,
        usdExchangeRate: 7,
        customCurrencySymbol: '€',
        customCurrencyExchangeRate: 0.8,
      },
    })

    expect(billingQuotaToAmount(1250000)).toBe(2.5)
    expect(billingAmountToQuota(2.5)).toBe(1250000)
    expect(billingQuotaToAmount(1250000, 'CNY')).toBe(2.5)
    expect(billingAmountToQuota(2.5, 'CNY')).toBe(1250000)
    expect(formatBillingQuota(1250000, 'USD', { locale: 'en-US' })).toBe('$2.5')
    expect(formatBillingQuota(1250000, 'CNY', { locale: 'en-US' })).toBe('¥2.5')
  }
)

test('honors each amount’s quota scale without reinterpreting the legacy payment display', () => {
  useSystemConfigStore.getState().setConfig({
    currency: {
      ...DEFAULT_CURRENCY_CONFIG,
      quotaDisplayType: 'CNY',
      quotaPerUnit: 1000000,
      usdExchangeRate: 7,
    },
  })

  expect(formatBillingQuota(2000000, 'USD', { locale: 'en-US' })).toBe('$2')
  expect(formatBillingQuota(2000000, 'CNY', { locale: 'en-US' })).toBe('¥4')
  expect(formatBillingQuota(2000000, 'USD', { locale: 'en-US' }, 500000)).toBe(
    '$4'
  )
  expect(formatBillingQuota(2000000, 'CNY', { locale: 'en-US' }, 1000000)).toBe(
    '¥2'
  )
  expect(billingQuotaToAmount(2000000)).toBe(2)
  expect(billingAmountToQuota(2)).toBe(2000000)
  expect(billingQuotaToAmount(2000000, 'USD', 500000)).toBe(4)
  expect(billingAmountToQuota(4, 'USD', 500000)).toBe(2000000)
  expect(billingQuotaToAmount(2000000, 'CNY', 1000000)).toBe(2)
  expect(billingAmountToQuota(2, 'CNY', 1000000)).toBe(2000000)
  expect(formatCurrencyFromUSD(2, { locale: 'en-US' })).toBe('¥14')
})
