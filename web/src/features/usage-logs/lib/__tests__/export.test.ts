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
import { createInstance } from 'i18next'
import { afterEach, describe, expect, test } from 'vitest'

import {
  DEFAULT_CURRENCY_CONFIG,
  useSystemConfigStore,
} from '@/stores/system-config-store'

import type { UsageLog } from '../../data/schema'
import type { WalletFundingLog } from '../../types'
import { getLogCsvHeader, getLogCsvRow, toCsvLine } from '../export'

const consumeLog: UsageLog = {
  id: 1,
  user_id: 7,
  created_at: new Date(2026, 9, 12, 8, 30, 5).getTime() / 1000,
  type: 2,
  content: '',
  username: 'alice',
  token_name: 'prod',
  model_name: 'gpt-4o-mini',
  quota: 250000,
  prompt_tokens: 2008,
  completion_tokens: 16,
  use_time: 3,
  is_stream: true,
  channel: 162,
  channel_name: 'azure',
  token_id: 5,
  group: 'default',
  ip: '203.0.113.9',
  other: JSON.stringify({
    cache_tokens: 1906,
    cache_creation_tokens_5m: 10,
    cache_creation_tokens_1h: 5,
  }),
  request_id: 'req-1',
  upstream_request_id: '',
}

const walletFunding: WalletFundingLog = {
  currency: 'CNY',
  status: 'settled',
  usd_quota: 250000,
  cny_quota: 1000000,
  usd_quota_per_unit: 250000,
  cny_quota_per_unit: 500000,
  debt_quota: 250000,
  exchange: {
    enabled: true,
    rate_id: 2,
    rate: '7',
    source: 'static',
    source_time: new Date(2026, 9, 12, 8, 0, 0).getTime() / 1000,
    policy_version: 3,
  },
}

async function chineseTranslator() {
  const i18n = createInstance()
  await i18n.init({
    lng: 'zh',
    resources: {
      zh: {
        translation: {
          Time: '时间',
          User: '用户',
          Channel: '渠道',
          Consume: '消费',
          'Top-up': '充值',
          Yes: '是',
          'Recharged {{amount}}': '充值 {{amount}}',
        },
      },
    },
  })
  return i18n.t
}

describe('usage log CSV export', () => {
  test('exports USD fallback debt in its frozen currency for CNY consumption', () => {
    const t = (key: string) => key
    const header = getLogCsvHeader(false, t)
    const row = getLogCsvRow(
      {
        ...consumeLog,
        other: JSON.stringify({
          wallet_funding: {
            ...walletFunding,
            usd_quota: 500000,
            cny_quota: 0,
            debt_quota: 125000,
            debt_currency: 'USD',
          },
        }),
      },
      false,
      t
    )
    expect(row[header.indexOf('USD wallet amount')]).toBe('$2')
    expect(row[header.indexOf('CNY wallet amount')]).toBe('¥0')
    expect(row[header.indexOf('Outstanding debt')]).toBe('$0.5')
    expect(row[header.indexOf('Debt currency')]).toBe('USD')
  })

  test('wallet funding preserves the existing log precision for tiny charges', () => {
    const t = (key: string) => key
    const header = getLogCsvHeader(false, t)
    const row = getLogCsvRow(
      {
        ...consumeLog,
        other: JSON.stringify({
          wallet_funding: {
            ...walletFunding,
            usd_quota: 1,
            cny_quota: 1,
            debt_quota: 1,
          },
        }),
      },
      false,
      t
    )
    expect(row[header.indexOf('USD wallet amount')]).toBe('$0.000004')
    expect(row[header.indexOf('CNY wallet amount')]).toBe('¥0.000002')
    expect(row[header.indexOf('Outstanding debt')]).toBe('¥0.000002')
  })

  test.each(['CNY', 'CUSTOM'] as const)(
    'exports wallet funding snapshots without applying %s display conversion',
    (quotaDisplayType) => {
      useSystemConfigStore.getState().setConfig({
        currency: {
          ...DEFAULT_CURRENCY_CONFIG,
          quotaDisplayType,
          quotaPerUnit: 1000000,
          usdExchangeRate: 14,
          customCurrencySymbol: '€',
          customCurrencyExchangeRate: 3,
        },
      })
      const t = (key: string) => key
      const header = getLogCsvHeader(false, t)
      const row = getLogCsvRow(
        {
          ...consumeLog,
          currency: 'CNY',
          quota: 4500000,
          other: JSON.stringify({
            quota_per_unit: 500000,
            wallet_funding: walletFunding,
          }),
        },
        false,
        t
      )
      expect(row).toHaveLength(header.length)
      expect(row[header.indexOf('Cost')]).toBe('¥9')
      expect(row[header.indexOf('USD wallet amount')]).toBe('$1')
      expect(row[header.indexOf('CNY wallet amount')]).toBe('¥2')
      expect(row[header.indexOf('Frozen exchange rate (CNY per USD)')]).toBe(
        '7'
      )
      expect(row[header.indexOf('Exchange rate source time')]).toBe(
        '2026-10-12 08:00:00'
      )
      expect(row[header.indexOf('Funding status')]).toBe('Settled')
      expect(row[header.indexOf('Outstanding debt')]).toBe('¥0.5')
      expect(row[header.indexOf('Debt currency')]).toBe('CNY')
    }
  )

  test.each([
    { status: 'reserved', label: 'Reserved' },
    { status: 'submitted', label: 'Held pending settlement' },
    { status: 'refunded', label: 'Refunded to original wallets' },
  ] as const)(
    'exports wallet funding $status state without calling it settled',
    ({ status, label }) => {
      const t = (key: string) => key
      const header = getLogCsvHeader(false, t)
      const row = getLogCsvRow(
        {
          ...consumeLog,
          other: JSON.stringify({
            wallet_funding: { ...walletFunding, status, debt_quota: 0 },
          }),
        },
        false,
        t
      )
      expect(row[header.indexOf('Funding status')]).toBe(label)
      expect(row[header.indexOf('USD wallet amount')]).toBe('$1')
      expect(row[header.indexOf('Outstanding debt')]).toBe('¥0')
    }
  )

  test('USD-priced funding reports CNY payment and USD debt using each recorded scale', () => {
    const t = (key: string) => key
    const header = getLogCsvHeader(false, t)
    const row = getLogCsvRow(
      {
        ...consumeLog,
        currency: 'USD',
        quota: 500000,
        other: JSON.stringify({
          quota_per_unit: 250000,
          wallet_funding: {
            ...walletFunding,
            currency: 'USD',
            cny_quota: 3500000,
            debt_quota: 125000,
          },
        }),
      },
      false,
      t
    )
    expect(row[header.indexOf('Cost')]).toBe('$2')
    expect(row[header.indexOf('USD wallet amount')]).toBe('$1')
    expect(row[header.indexOf('CNY wallet amount')]).toBe('¥7')
    expect(row[header.indexOf('Frozen exchange rate (CNY per USD)')]).toBe('7')
    expect(row[header.indexOf('Outstanding debt')]).toBe('$0.5')
    expect(row[header.indexOf('Debt currency')]).toBe('USD')
  })

  test('native wallet funding exports zero accurately and leaves unused exchange fields empty', () => {
    const t = (key: string) => key
    const header = getLogCsvHeader(false, t)
    const row = getLogCsvRow(
      {
        ...consumeLog,
        other: JSON.stringify({
          wallet_funding: {
            ...walletFunding,
            currency: 'USD',
            usd_quota: 0,
            cny_quota: 0,
            debt_quota: 0,
          },
        }),
      },
      false,
      t
    )
    expect(row[header.indexOf('USD wallet amount')]).toBe('$0')
    expect(row[header.indexOf('CNY wallet amount')]).toBe('¥0')
    expect(row[header.indexOf('Frozen exchange rate (CNY per USD)')]).toBe('')
    expect(row[header.indexOf('Exchange rate source time')]).toBe('')
  })

  test('legacy logs leave every missing wallet receipt column empty', () => {
    const t = (key: string) => key
    const header = getLogCsvHeader(false, t)
    const row = getLogCsvRow(consumeLog, false, t)
    for (const column of [
      'USD wallet amount',
      'CNY wallet amount',
      'Frozen exchange rate (CNY per USD)',
      'Exchange rate source time',
      'Funding status',
      'Outstanding debt',
      'Debt currency',
    ]) {
      expect(row[header.indexOf(column)]).toBe('')
    }
  })
  test.each([
    { delta: 500000, after: 1000000, amount: '+¥1' },
    { delta: -250000, after: 250000, amount: '-¥0.5' },
  ])(
    'exports native adjustment $amount with both balances and reason',
    (scenario) => {
      const t = (key: string) => key
      const header = getLogCsvHeader(false, t)
      const log: UsageLog = {
        ...consumeLog,
        type: 3,
        currency: 'CNY',
        quota: scenario.delta,
        other: JSON.stringify({
          delta_quota: scenario.delta,
          before_quota: 500000,
          after_quota: scenario.after,
          reason: 'Reconciliation',
        }),
      }
      const row = getLogCsvRow(log, false, t)
      expect(row).toHaveLength(header.length)
      expect(row[header.indexOf('Cost')]).toBe(scenario.amount)
      expect(row[header.indexOf('Quota before adjustment')]).toBe('¥1')
      expect(row[header.indexOf('Quota after adjustment')]).toBe(
        `¥${scenario.after / 500000}`
      )
      expect(row[header.indexOf('Reason')]).toBe('Reconciliation')
      expect(row[header.indexOf('Input Tokens')]).toBe('')
      for (const legacy of [
        { ...log, currency: 'USD' as const },
        { ...log, other: '{}' },
      ]) {
        const legacyRow = getLogCsvRow(legacy, false, t)
        for (const key of [
          'Cost',
          'Quota before adjustment',
          'Quota after adjustment',
          'Reason',
        ]) {
          expect(legacyRow[header.indexOf(key)]).toBe('')
        }
      }
    }
  )
  test('exports native CNY and preserves pending reservation separately from settled cost', () => {
    const t = (key: string) => key
    const header = getLogCsvHeader(false, t)
    const row = getLogCsvRow(
      {
        ...consumeLog,
        currency: 'CNY',
        quota: 0,
        other: JSON.stringify({
          billing_status: 'reserved',
          reserved_quota: 250000,
        }),
      },
      false,
      t
    )
    expect(row[header.indexOf('Billing currency')]).toBe('CNY')
    expect(row[header.indexOf('Billing status')]).toBe('Reserved')
    expect(row[header.indexOf('Cost')]).toBe('¥0')
    expect(row[header.indexOf('Reserved')]).toBe('¥0.5')
  })
  test('header in the user view has no user or channel column', async () => {
    const t = await chineseTranslator()

    const header = getLogCsvHeader(false, t)

    expect(header[0]).toBe('时间')
    expect(header).not.toContain('用户')
    expect(header).not.toContain('渠道')
  })

  test('administrator row shows the user and channel next to the matching header', async () => {
    const t = await chineseTranslator()

    const header = getLogCsvHeader(true, t)
    const row = getLogCsvRow(consumeLog, true, t)

    expect(row).toHaveLength(header.length)
    expect(row[header.indexOf('用户')]).toBe('alice')
    expect(row[header.indexOf('渠道')]).toBe('#162 azure')
  })

  test('consume log row matches the log table values', async () => {
    const t = await chineseTranslator()

    expect(getLogCsvRow(consumeLog, false, t)).toEqual([
      '2026-10-12 08:30:05',
      '消费',
      'prod',
      'default',
      'gpt-4o-mini',
      '是',
      '2008',
      '16',
      '1906',
      '15',
      '$0.5',
      '3s',
      '203.0.113.9',
      'req-1',
      '',
      'USD',
      '',
      '',
      '',
      '',
      '',
      '',
      '',
      '',
      '',
      '',
      '',
      '',
    ])
  })

  test('cache write cell counts OpenAI 12-hour writes once as part of the total', async () => {
    const t = await chineseTranslator()
    const header = getLogCsvHeader(false, t)

    const row = getLogCsvRow(
      {
        ...consumeLog,
        other: JSON.stringify({
          cache_tokens: 1906,
          cache_creation_tokens: 1500,
          cache_creation_ratio: 1.25,
          cache_creation_tokens_12h: 1000,
          cache_creation_ratio_12h: 2.5,
        }),
      },
      false,
      t
    )

    expect(row[header.indexOf('Cache Write')]).toBe('1500')
  })

  test('top-up log row leaves usage cells empty and shows the translated content', async () => {
    const t = await chineseTranslator()
    const topup: UsageLog = {
      ...consumeLog,
      type: 1,
      content: 'Recharged $5',
      other: JSON.stringify({
        content_parts: [
          { key: 'Recharged {{amount}}', params: { amount: '$5' } },
        ],
      }),
    }

    const row = getLogCsvRow(topup, false, t)

    expect(row.slice(1, 2)).toEqual(['充值'])
    expect(row.slice(5, 12)).toEqual(['', '', '', '', '', '', ''])
    expect(row[getLogCsvHeader(false, t).indexOf('Details')]).toBe('充值 $5')
  })

  test('cells are quoted and text that starts a formula stays text', () => {
    expect(
      toCsvLine(['=HYPERLINK("x")', '-12.5', '+cmd', 'a"b', 'two\nlines'])
    ).toBe(`"'=HYPERLINK(""x"")","-12.5","'+cmd","a""b","two\nlines"\r\n`)
  })
})

afterEach(() => {
  useSystemConfigStore
    .getState()
    .setConfig({ currency: { ...DEFAULT_CURRENCY_CONFIG } })
})

test.each(['USD', 'CNY'] as const)(
  'exports %s amounts using their recorded scale without display FX conversion',
  (currency) => {
    useSystemConfigStore.getState().setConfig({
      currency: {
        ...DEFAULT_CURRENCY_CONFIG,
        quotaDisplayType: 'CNY',
        usdExchangeRate: 7,
        quotaPerUnit: 1000000,
      },
    })
    const t = (key: string) => key
    const header = getLogCsvHeader(false, t)
    const row = getLogCsvRow(
      {
        ...consumeLog,
        currency,
        other: JSON.stringify({ quota_per_unit: 250000 }),
      },
      false,
      t
    )
    expect(row[header.indexOf('Cost')]).toBe(currency === 'USD' ? '$1' : '¥1')
  }
)
