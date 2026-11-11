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
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, render, screen } from '@testing-library/react'
import { createInstance } from 'i18next'
import { I18nextProvider, initReactI18next } from 'react-i18next'
import { beforeEach, expect, it, vi } from 'vitest'

import { BillingHistoryDialog } from '@/features/wallet/components/dialogs/billing-history-dialog'
import en from '@/i18n/locales/en.json'
import zh from '@/i18n/locales/zh.json'
import { api } from '@/lib/api'
import {
  DEFAULT_CURRENCY_CONFIG,
  useSystemConfigStore,
} from '@/stores/system-config-store'

import { RechargeFormCard } from '../components/recharge-form-card'
import { WalletStatsCard } from '../components/wallet-stats-card'

const i18n = createInstance()

beforeEach(async () => {
  await i18n.use(initReactI18next).init({
    lng: 'zh',
    fallbackLng: false,
    nsSeparator: false,
    resources: { en, zh },
    interpolation: { escapeValue: false },
  })
})

it('translates billing history statuses and updates them when the language changes', async () => {
  vi.spyOn(api, 'get').mockResolvedValue({
    data: {
      success: true,
      data: {
        total: 3,
        items: ['success', 'pending', 'expired'].map((status, id) => ({
          id,
          status,
          amount: 10,
          money: 10,
          trade_no: `order-${id}`,
          payment_method: 'stripe',
          create_time: 1,
        })),
      },
    },
  })
  render(
    <I18nextProvider i18n={i18n}>
      <BillingHistoryDialog open onOpenChange={vi.fn()} />
    </I18nextProvider>
  )
  await screen.findByText('order-0')
  for (const label of ['成功', '待确认', '已过期']) {
    expect(screen.getByText(label)).toBeVisible()
  }
  await act(() => i18n.changeLanguage('en'))
  for (const label of ['Success', 'Pending', 'Expired']) {
    expect(screen.getByText(label)).toBeVisible()
  }
})

it.each([
  { priceRatio: 7.3, paid: '584', saved: '146', full: '146' },
  { priceRatio: 0.000123, paid: '0.0098', saved: '0.0025', full: '0.0025' },
])(
  'translates recharge presets and minimum amount without changing precision at price ratio $priceRatio',
  async ({ priceRatio, paid, saved, full }) => {
    render(
      <I18nextProvider i18n={i18n}>
        <RechargeFormCard
          topupInfo={{
            enable_online_topup: true,
            enable_stripe_topup: false,
            pay_methods: [],
            min_topup: 1,
            stripe_min_topup: 1,
            amount_options: [100, 20],
            discount: {},
          }}
          presetAmounts={[{ value: 100, discount: 0.8 }, { value: 20 }]}
          selectedPreset={null}
          topupAmount={1}
          paymentAmount={priceRatio}
          priceRatio={priceRatio}
          calculating={false}
          paymentLoading={null}
          redemptionCode=''
          redeeming={false}
          onSelectPreset={vi.fn()}
          onTopupAmountChange={vi.fn()}
          onPaymentMethodSelect={vi.fn()}
          onRedemptionCodeChange={vi.fn()}
          onRedeem={vi.fn()}
        />
      </I18nextProvider>
    )
    expect(screen.getByRole('button', { name: /^100 / })).toHaveTextContent(
      `优惠 20%支付 ${paid} • 节省 ${saved}`
    )
    expect(screen.getByRole('button', { name: /^20 / })).toHaveTextContent(
      `支付 ${full}`
    )
    expect(screen.getByPlaceholderText('最低 1')).toBeVisible()
    await act(() => i18n.changeLanguage('en'))
    expect(screen.getByRole('button', { name: /^100 / })).toHaveTextContent(
      `20% OFFPay ${paid} • Save ${saved}`
    )
    expect(screen.getByRole('button', { name: /^20 / })).toHaveTextContent(
      `Pay ${full}`
    )
    expect(screen.getByPlaceholderText('Minimum 1')).toBeVisible()
  }
)

it('keeps USD and CNY wallet balances distinct when the reference display mode changes', async () => {
  await i18n.changeLanguage('en')
  const previousConfig = useSystemConfigStore.getState().config
  const client = new QueryClient({
    defaultOptions: { queries: { staleTime: Infinity } },
  })
  client.setQueryData(['currency-wallet', 'self', 'CNY'], {
    currency: 'CNY',
    quota: 3000000,
    used_quota: 1500000,
    quota_per_unit: 1000000,
  })
  const user = {
    id: 1,
    username: 'user',
    quota: 2500000,
    used_quota: 1250000,
    request_count: 1234,
    aff_quota: 0,
    aff_history_quota: 0,
    aff_count: 0,
    group: 'default',
  }
  try {
    useSystemConfigStore.getState().setConfig({
      currency: {
        ...DEFAULT_CURRENCY_CONFIG,
        quotaDisplayType: 'CNY',
        usdExchangeRate: 7,
      },
    })
    render(
      <I18nextProvider i18n={i18n}>
        <QueryClientProvider client={client}>
          <WalletStatsCard user={user} />
        </QueryClientProvider>
      </I18nextProvider>
    )
    for (const label of [
      'USD balance',
      'USD consumption',
      'CNY balance',
      'CNY consumption',
    ]) {
      expect(screen.getAllByText(label)).toHaveLength(1)
    }
    for (const quotaDisplayType of ['CNY', 'CUSTOM', 'TOKENS'] as const) {
      await act(() => {
        useSystemConfigStore.getState().setConfig({
          currency: {
            ...DEFAULT_CURRENCY_CONFIG,
            quotaDisplayType,
            usdExchangeRate: 14,
            customCurrencySymbol: '€',
            customCurrencyExchangeRate: 0.8,
          },
        })
      })
      for (const value of ['$5', '$2.5', '¥3', '¥1.5']) {
        expect(screen.getByText(value)).toBeVisible()
      }
    }
  } finally {
    client.clear()
    useSystemConfigStore.getState().setConfig(previousConfig)
  }
})
