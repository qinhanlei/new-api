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
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, expect, it, vi } from 'vitest'

import { ModelPriceCell } from '@/features/pricing/components/model-price-cell'
import { SORT_OPTIONS } from '@/features/pricing/constants'
import {
  combineBillingExpr,
  splitBillingExprAndRequestRules,
} from '@/features/pricing/lib/billing-expr'
import { compileBillingExpression } from '@/features/pricing/lib/billing-expression/parser'
import { evaluateBillingExpression } from '@/features/pricing/lib/billing-expression/runtime'
import {
  parseVisualBillingDocument,
  serializeVisualBillingDocument,
} from '@/features/pricing/lib/billing-expression/visual'
import { getDynamicPricingSummary } from '@/features/pricing/lib/dynamic-price'
import { sortModels } from '@/features/pricing/lib/filters'
import {
  generateTaskExprFromConfig,
  tryParseTaskVisualConfig,
} from '@/features/pricing/lib/task-expr'
import {
  generateExprFromVisualConfig,
  tryParseVisualConfig,
} from '@/features/pricing/lib/tier-expr'
import type { PricingModel } from '@/features/pricing/types'
import { TaskUsagePricingEditor } from '@/features/system-settings/models/task-usage-pricing-editor'
import { UserQuotaDialog } from '@/features/users/components/user-quota-dialog'
import { toIntlLocale } from '@/i18n/languages'
import { api } from '@/lib/api'
import { formatBillingQuota, formatNativeMoney } from '@/lib/currency'
import {
  DEFAULT_CURRENCY_CONFIG,
  useSystemConfigStore,
} from '@/stores/system-config-store'

import { BillingCurrencySelector } from '../billing-currency-selector'
import { CNY_PRICING_CURRENCY } from '../currency'
import { applyPriceSyncSelections, pricingOptions } from '../pricing'

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
  useSystemConfigStore
    .getState()
    .setConfig({ currency: { ...DEFAULT_CURRENCY_CONFIG } })
})

it.each([
  ['en', '¥12.5'],
  ['zhCN', '¥12.5'],
  ['zhTW', '¥12.5'],
  ['fr', '12,5\u00a0¥'],
  ['ru', '12,5\u00a0¥'],
  ['ja', '￥12.5'],
  ['vi', '12,5\u00a0¥'],
])('formats native money for interface language %s', (language, expected) => {
  expect(
    formatNativeMoney(12.5, 'CNY', { locale: toIntlLocale(language) })
  ).toBe(expected)
})

it('uses the runtime locale when an invalid language is normalized', () => {
  const locale = toIntlLocale('invalid_locale@')
  expect(locale).toBeUndefined()
  expect(formatNativeMoney(12.5, 'CNY', { locale })).toBe(
    formatNativeMoney(12.5, 'CNY')
  )
})

it('preserves native currency through parsing, request rules and visual round trips', () => {
  const source = 'v2:CNY:tier("base", p * 2 + c * 8)'
  const rules = '(header("x-fast") == "yes" ? 2 : 1)'
  expect(compileBillingExpression(source)).toMatchObject({
    status: 'ready',
    version: 2,
    currency: 'CNY',
  })
  expect(compileBillingExpression('v2:EUR:tier("base", p * 2)')).toMatchObject({
    status: 'unsupported',
  })
  const combined = combineBillingExpr(source, rules)
  expect(combined).toBe(`v2:CNY:(tier("base", p * 2 + c * 8)) * ${rules}`)
  expect(splitBillingExprAndRequestRules(combined)).toEqual({
    billingExpr: source,
    requestRuleExpr: rules,
  })
  expect(
    evaluateBillingExpression(combined, {
      tokens: { p: 100, c: 20 },
      request: { headers: { 'x-fast': 'yes' } },
    })
  ).toMatchObject({ status: 'success', cost: 720 })
  expect(generateExprFromVisualConfig(tryParseVisualConfig(source))).toBe(
    source
  )
  const visual = parseVisualBillingDocument(source)
  expect(visual).not.toBeNull()
  if (visual) {
    expect(serializeVisualBillingDocument(visual)).toEqual({ ok: true, source })
  }
  const task = 'v2:CNY:tier("base", u("tokens") * 10.43 / 1000000)'
  const schema = { tokens: { type: 'number' as const, unit: 'token' as const } }
  expect(
    generateTaskExprFromConfig(tryParseTaskVisualConfig(task, schema), schema)
  ).toBe(task)
})

it('keeps native prices and historical quota stable when display and recharge rates change', () => {
  const model: PricingModel = {
    id: 1,
    model_name: 'native',
    quota_type: 0,
    model_ratio: 1,
    completion_ratio: 1,
    enable_groups: ['default'],
    billing_mode: 'tiered_expr',
    billing_expr: 'v2:CNY:tier("base", p * 2 + c * 8)',
  }
  for (const rate of [1, 7, 19]) {
    useSystemConfigStore.getState().setConfig({
      currency: {
        ...DEFAULT_CURRENCY_CONFIG,
        quotaDisplayType: 'CNY',
        usdExchangeRate: rate,
      },
    })
    expect(formatBillingQuota(500000, 'CNY')).toBe(formatNativeMoney(1, 'CNY'))
    const summary = getDynamicPricingSummary(model, {
      tokenUnit: 'M',
      showRechargePrice: true,
      priceRate: 3,
      usdExchangeRate: rate,
      groupRatioMultiplier: 2,
    })
    expect(
      summary?.entries.find((entry) => entry.field === 'inputPrice')?.formatted
    ).toBe(formatNativeMoney(4, 'CNY'))
  }
})

it('labels single and mixed provider overrides with each displayed price currency', () => {
  const model: PricingModel = {
    id: 1,
    model_name: 'provider-currency',
    quota_type: 0,
    model_ratio: 1,
    completion_ratio: 1,
    enable_groups: ['default'],
    billing_plugin_variants: [
      {
        plugin_key: 'native',
        plugin_name: 'Native',
        billing_expr: 'v2:CNY:tier("base", u("seconds") * 0.4)',
        billing_usage_schema: { seconds: { type: 'number', unit: 'second' } },
      },
    ],
  }
  const { rerender } = render(<ModelPriceCell model={model} />)
  expect(screen.getByText('CNY')).toBeVisible()
  expect(screen.getByText('0.4/s')).toBeVisible()
  expect(screen.queryByText('USD')).not.toBeInTheDocument()

  model.billing_plugin_variants = [
    ...(model.billing_plugin_variants ?? []),
    {
      plugin_key: 'dollar',
      plugin_name: 'Dollar',
      billing_expr: 'tier("base", u("seconds") * 0.2)',
      billing_usage_schema: { seconds: { type: 'number', unit: 'second' } },
    },
  ]
  rerender(<ModelPriceCell model={{ ...model }} />)
  expect(screen.getByText('CNY 0.4/s')).toBeVisible()
  expect(screen.getByText('USD 0.2/s')).toBeVisible()
  useSystemConfigStore.getState().setConfig({
    currency: {
      ...DEFAULT_CURRENCY_CONFIG,
      quotaDisplayType: 'CNY',
      usdExchangeRate: 7,
    },
  })
  rerender(
    <ModelPriceCell model={{ ...model }} options={{ usdExchangeRate: 7 }} />
  )
  expect(screen.getByText('CNY 0.4/s')).toBeVisible()
  expect(screen.getByText('CNY 1.4/s')).toBeVisible()
})

it('rejects cross-currency synchronization rather than relabeling existing prices', () => {
  const options = pricingOptions({
    'billing_setting.billing_mode': JSON.stringify({ native: 'tiered_expr' }),
    'billing_setting.billing_expr': JSON.stringify({
      native: 'v2:CNY:tier("base", p * 2 + c * 8)',
    }),
  })
  expect(() =>
    applyPriceSyncSelections(options, {
      native: { billing_expr: 'tier("base", p * 2 + c * 8)' },
    })
  ).toThrow('Cannot sync prices across billing currencies')
})

it('sorts prices within each currency without comparing CNY amounts to USD amounts', () => {
  const base: PricingModel = {
    id: 1,
    model_name: 'one',
    model_ratio: 1,
    completion_ratio: 1,
    quota_type: 0,
    enable_groups: ['default'],
    billing_mode: 'tiered_expr',
  }
  const models = [
    { ...base, model_name: 'usd', billing_expr: 'tier("base", p * 1 + c * 2)' },
    {
      ...base,
      model_name: 'cny-high',
      billing_expr: 'v2:CNY:tier("base", p * 9 + c * 18)',
    },
    {
      ...base,
      model_name: 'cny-low',
      billing_expr: 'v2:CNY:tier("base", p * 3 + c * 6)',
    },
  ]
  expect(
    sortModels(models, SORT_OPTIONS.INPUT_PRICE_LOW).map(
      (model) => model.model_name
    )
  ).toEqual(['cny-low', 'cny-high', 'usd'])
})

it('requires confirmation and clears prices when selecting another billing currency', async () => {
  const onChange = vi.fn()
  render(
    <BillingCurrencySelector
      expression='tier("base", p * 5)'
      defaultExpression='tier("base", p * 0)'
      onChange={onChange}
    />
  )
  const user = userEvent.setup()
  await user.click(screen.getByRole('combobox', { name: 'Billing currency' }))
  await user.click(screen.getByRole('option', { name: 'Chinese yuan (CNY)' }))
  expect(onChange).not.toHaveBeenCalled()
  await user.click(
    screen.getByRole('button', { name: 'Clear prices and continue' })
  )
  expect(onChange).toHaveBeenCalledWith('v2:CNY:tier("base", p * 0)')
})

it('edits task prices in native CNY and retains the currency prefix', () => {
  const change = vi.fn()
  render(
    <TaskUsagePricingEditor
      currency={{ ...CNY_PRICING_CURRENCY, exchangeRate: 7 }}
      billingExpr='v2:CNY:tier("base", u("seconds") * 0.4)'
      requestRuleExpr=''
      usageSchema={{
        seconds: {
          type: 'number',
          unit: 'second',
          description: 'Video unit price',
        },
      }}
      onBillingExprChange={change}
      onRequestRuleExprChange={vi.fn()}
    />
  )
  const input = screen.getByRole('textbox', { name: 'seconds' })
  expect(input).toHaveValue('0.4')
  fireEvent.change(input, { target: { value: '0.8' } })
  fireEvent.blur(input)
  expect(change.mock.lastCall?.[0]).toBe(
    'v2:CNY:tier("base", u("seconds") * 0.8)'
  )
})

it('reuses the adjustment operation ID after a lost response and sends native CNY quota', async () => {
  vi.spyOn(api, 'get').mockResolvedValue({
    data: {
      success: true,
      data: {
        currency: 'CNY',
        quota: 500000,
        used_quota: 0,
        quota_per_unit: 500000,
      },
    },
  })
  const post = vi
    .spyOn(api, 'post')
    .mockRejectedValueOnce(new Error('Connection lost'))
    .mockResolvedValueOnce({
      data: {
        success: true,
        data: {
          currency: 'CNY',
          quota: 1125000,
          used_quota: 0,
          quota_per_unit: 500000,
        },
      },
    })
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  render(
    <QueryClientProvider client={client}>
      <UserQuotaDialog
        open
        onOpenChange={vi.fn()}
        userId={9}
        currentQuota={500000}
        onSuccess={vi.fn()}
      />
    </QueryClientProvider>
  )
  const user = userEvent.setup()
  await user.click(screen.getByRole('combobox', { name: 'Billing currency' }))
  await user.click(screen.getByRole('option', { name: 'CNY' }))
  await user.type(screen.getByRole('spinbutton', { name: 'Amount' }), '1.25')
  expect(
    screen.queryByRole('textbox', { name: 'Reason' })
  ).not.toBeInTheDocument()
  const confirm = screen.getByRole('button', { name: 'Confirm' })
  await waitFor(() => expect(confirm).toBeEnabled())
  await user.click(confirm)
  await waitFor(() => expect(post).toHaveBeenCalledTimes(1))
  await waitFor(() => expect(confirm).toBeEnabled())
  await user.click(confirm)
  await waitFor(() => expect(post).toHaveBeenCalledTimes(2))
  const first = post.mock.calls[0]
  expect(first[0]).toBe('/api/user/9/currency/CNY')
  expect(first[1]).toMatchObject({
    mode: 'add',
    value: 625000,
    operation_id: expect.stringMatching(/^cny-[A-Za-z0-9_-]+$/),
  })
  expect(post.mock.calls[1][1]).toEqual(first[1])
  client.clear()
})
