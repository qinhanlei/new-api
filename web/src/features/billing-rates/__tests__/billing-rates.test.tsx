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
  act,
  cleanup,
  render,
  renderHook,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, expect, it, vi } from 'vitest'

import { usePricingData } from '@/features/pricing/hooks/use-pricing-data'
import {
  getDisplayGroupRatio,
  getModelBillingRate,
} from '@/features/pricing/lib/model-helpers'
import {
  formatFixedPrice,
  formatGroupPrice,
} from '@/features/pricing/lib/price'
import type { PricingModel } from '@/features/pricing/types'
import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth-store'

import { BillingRatesEditor } from '../billing-rates-editor'
import { billingRateChanges, billingRateInputSchema } from '../rates'

let client: QueryClient
const originalAdapter = api.defaults.adapter
afterEach(() => {
  cleanup()
  client?.clear()
  useAuthStore.getState().auth.reset()
  api.defaults.adapter = originalAdapter
  vi.restoreAllMocks()
})

const model: PricingModel = {
  id: 1,
  model_name: 'model-a',
  billing_model_name: 'model-a',
  quota_type: 0,
  model_ratio: 5,
  completion_ratio: 2,
  enable_groups: ['default', 'vip'],
  group_ratio: { default: 0.9, vip: 0 },
  effective_rates: {
    default: { rate: 0.6, source: 'user', version: 2, model_name: 'model-a' },
    vip: { rate: 0.6, source: 'user', version: 2, model_name: 'model-a' },
  },
}

it('uses the final user rate for token and fixed prices, including a free group', () => {
  expect(getDisplayGroupRatio(model)).toBe(0.6)
  expect(getModelBillingRate(model, 'vip')).toBe(0.6)
  expect(
    formatGroupPrice(
      model,
      'default',
      'input',
      'M',
      false,
      1,
      1,
      model.group_ratio ?? {}
    )
  ).toBe('$6')
  expect(
    formatFixedPrice(
      { ...model, quota_type: 1, model_price: 10 },
      'vip',
      false,
      1,
      1,
      model.group_ratio ?? {}
    )
  ).toBe('$6')
  const free: PricingModel = {
    ...model,
    effective_rates: {
      default: { rate: 0, source: 'user', version: 2, model_name: 'model-a' },
    },
  }
  expect(getModelBillingRate(free, 'default')).toBe(0)
})

it('preserves inherited, explicit free, full price, and cleared rule versions', () => {
  expect(
    billingRateChanges([{ model_name: 'model-a', rate: null, version: 3 }], {
      'model-a': '0',
      'model-b': '1',
      'model-c': '',
    })
  ).toEqual([
    { model_name: 'model-a', rate: '0', expected_version: 3 },
    { model_name: 'model-b', rate: '1', expected_version: 0 },
  ])
  expect(
    billingRateChanges([{ model_name: 'model-a', rate: '0', version: 4 }], {
      'model-a': '',
    })
  ).toEqual([{ model_name: 'model-a', rate: null, expected_version: 4 }])
  for (const invalid of [
    '-1',
    'NaN',
    'Infinity',
    '1e2',
    '0.0000001',
    '100.000001',
  ]) {
    expect(billingRateInputSchema.safeParse(invalid).success).toBe(false)
  }
})

it('keeps a rejected rate draft and saves an explicit free rate after confirmation', async () => {
  useAuthStore.getState().auth.setUser({ id: 1, username: 'root', role: 100 })
  client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  let rate = '0.8'
  let reject = true
  const changes: unknown[] = []
  api.defaults.adapter = async (config) => {
    let data: unknown = { success: true, data: {} }
    if (config.url === '/api/pricing') {
      data = {
        success: true,
        data: [model],
        vendors: [],
        group_ratio: { default: 0.9 },
      }
    }
    if (config.url === '/api/option/billing_rates') {
      if (config.method === 'patch') {
        const body = JSON.parse(config.data)
        changes.push(body)
        if (reject) {
          return {
            data: {
              success: false,
              message: 'Billing rates changed; reload before saving',
            },
            status: 200,
            statusText: 'OK',
            headers: {},
            config,
          }
        }
        rate = body.changes[0].rate
      }
      data = {
        success: true,
        data: {
          billing_model_name: 'model-a',
          rules: [
            { model_name: 'model-a', rate, version: rate === '0.8' ? 2 : 3 },
          ],
          global_rules: [],
        },
      }
    }
    return { data, status: 200, statusText: 'OK', headers: {}, config }
  }
  render(
    <QueryClientProvider client={client}>
      <BillingRatesEditor modelName='model-a' />
    </QueryClientProvider>
  )
  const user = userEvent.setup()
  const input = await screen.findByRole('textbox', { name: 'Rate for model-a' })
  await user.clear(input)
  await user.type(input, '0')
  await user.click(screen.getByRole('button', { name: 'Save rates' }))
  await user.click(
    within(screen.getByRole('alertdialog')).getByRole('button', {
      name: 'Save rates',
    })
  )
  expect(
    await within(screen.getByRole('alertdialog')).findByRole('alert')
  ).toHaveTextContent('Billing rates changed; reload before saving')
  expect(input).toHaveValue('0')
  reject = false
  await user.click(
    within(screen.getByRole('alertdialog')).getByRole('button', {
      name: 'Save rates',
    })
  )
  await waitFor(() =>
    expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
  )
  expect(changes.at(-1)).toEqual({
    changes: [{ model_name: 'model-a', rate: '0', expected_version: 2 }],
  })
  expect(screen.getByRole('button', { name: 'Save rates' })).toBeDisabled()
})

it('does not expose rate controls or request private rules for a non-root user', () => {
  useAuthStore
    .getState()
    .auth.setUser({ id: 2, username: 'administrator', role: 10 })
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  const get = vi
    .spyOn(api, 'get')
    .mockResolvedValue({ data: { success: true, data: {} } })
  render(
    <QueryClientProvider client={client}>
      <BillingRatesEditor userId={3} />
    </QueryClientProvider>
  )
  expect(
    screen.queryByRole('button', { name: 'Save rates' })
  ).not.toBeInTheDocument()
  expect(get.mock.calls.some(([url]) => url.includes('billing_rates'))).toBe(
    false
  )
})

it('drops the old user price immediately when switching accounts and removes its cached price', async () => {
  useAuthStore.getState().auth.setUser({ id: 7, username: 'first', role: 1 })
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  client.setQueryData(['status'], {})
  client.setQueryData(['pricing', 7], {
    success: true,
    data: [model],
    vendors: [],
    group_ratio: {},
  })
  const get = vi.spyOn(api, 'get').mockImplementation(async (_url, config) => {
    if (config?.signal?.aborted) throw new Error('cancelled')
    return {
      data: {
        success: true,
        data: [{ ...model, model_name: 'second-price' }],
        vendors: [],
        group_ratio: {},
      },
    }
  })
  const { result } = renderHook(() => usePricingData(), {
    wrapper: ({ children }) => (
      <QueryClientProvider client={client}>{children}</QueryClientProvider>
    ),
  })
  expect(result.current.models[0].model_name).toBe('model-a')
  act(() =>
    useAuthStore.getState().auth.setUser({ id: 8, username: 'second', role: 1 })
  )
  expect(
    result.current.models.some((entry) => entry.model_name === 'model-a')
  ).toBe(false)
  await waitFor(() =>
    expect(result.current.models[0]?.model_name).toBe('second-price')
  )
  expect(get).toHaveBeenCalledWith(
    '/api/pricing',
    expect.objectContaining({
      disableDuplicate: true,
      signal: expect.anything(),
    })
  )
  await waitFor(() =>
    expect(client.getQueryData(['pricing', 7])).toBeUndefined()
  )
})

it('applies a batch rate only to selected models and restores inheritance without turning it into zero', async () => {
  useAuthStore.getState().auth.setUser({ id: 1, username: 'root', role: 100 })
  client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  const rules = [
    { model_name: 'model-a', rate: '0.6', version: 3 },
    { model_name: 'model-b', rate: '0.5', version: 2 },
  ]
  vi.spyOn(api, 'get').mockImplementation(async (url) => {
    if (url.endsWith('/billing_rates')) {
      return { data: { success: true, data: { rules, global_rules: [] } } }
    }
    if (url === '/api/pricing') {
      return {
        data: { success: true, data: [model], vendors: [], group_ratio: {} },
      }
    }
    return { data: { success: true, data: {} } }
  })
  const patch = vi.spyOn(api, 'patch').mockResolvedValue({
    data: {
      success: true,
      data: {
        rules: [rules[0], { ...rules[1], rate: null, version: 3 }],
        global_rules: [],
      },
    },
  })
  render(
    <QueryClientProvider client={client}>
      <BillingRatesEditor userId={42} />
    </QueryClientProvider>
  )
  const user = userEvent.setup()
  await user.click(
    await screen.findByRole('checkbox', { name: 'Select model-b' })
  )
  const batch = screen.getByRole('textbox', {
    name: 'Rate for 1 selected models',
  })
  await user.type(batch, '0')
  await user.click(screen.getByRole('button', { name: 'Apply' }))
  expect(screen.getByRole('textbox', { name: 'Rate for model-b' })).toHaveValue(
    '0'
  )
  expect(screen.getByRole('textbox', { name: 'Rate for model-a' })).toHaveValue(
    '0.6'
  )
  await user.click(screen.getByRole('button', { name: 'Restore inheritance' }))
  expect(screen.getByRole('textbox', { name: 'Rate for model-b' })).toHaveValue(
    ''
  )
  await user.click(screen.getByRole('button', { name: 'Save rates' }))
  await user.click(
    within(screen.getByRole('alertdialog')).getByRole('button', {
      name: 'Save rates',
    })
  )
  await waitFor(() =>
    expect(patch).toHaveBeenCalledWith(
      '/api/user/42/billing_rates',
      { changes: [{ model_name: 'model-b', rate: null, expected_version: 2 }] },
      { params: undefined }
    )
  )
})

it.each([
  {
    source: 'effective',
    effective_rates: {
      default: {
        rate: 2,
        source: 'user' as const,
        version: 1,
        model_name: 'model-a',
      },
    },
    group_ratio: {} as Record<string, number>,
  },
  { source: 'legacy', effective_rates: undefined, group_ratio: { default: 2 } },
])(
  'uses only the available $source groups for the minimum price',
  ({ effective_rates, group_ratio }) => {
    expect(
      getDisplayGroupRatio({ ...model, effective_rates, group_ratio })
    ).toBe(2)
  }
)

it.each([
  { userId: 0, pricing: 'failed' },
  { userId: 42, pricing: 'empty' },
  { userId: 0, pricing: 'conflicting' },
])(
  'saves the server billing identity for user $userId when public pricing is $pricing',
  async ({ userId, pricing }) => {
    useAuthStore.getState().auth.setUser({ id: 1, username: 'root', role: 100 })
    client = new QueryClient({
      defaultOptions: {
        queries: { retry: false },
        mutations: { retry: false },
      },
    })
    const path = userId
      ? `/api/user/${userId}/billing_rates`
      : '/api/option/billing_rates'
    let config = {
      billing_model_name: 'server-billing-model',
      rules: [{ model_name: 'server-billing-model', rate: '0.8', version: 4 }],
      global_rules: [],
    }
    const get = vi.spyOn(api, 'get').mockImplementation(async (url) => {
      if (url === path) return { data: { success: true, data: config } }
      if (url === '/api/pricing') {
        if (pricing === 'failed') throw new Error('Pricing unavailable')
        return {
          data: {
            success: true,
            data:
              pricing === 'conflicting'
                ? [
                    {
                      ...model,
                      model_name: 'server-billing-model',
                      billing_model_name: 'wrong-model',
                    },
                  ]
                : [],
            vendors: [],
            group_ratio: {},
          },
        }
      }
      return { data: { success: true, data: {} } }
    })
    const patch = vi.spyOn(api, 'patch').mockImplementation(async () => {
      config = {
        ...config,
        rules: [
          { model_name: 'server-billing-model', rate: '0.6', version: 5 },
        ],
      }
      return { data: { success: true, data: config } }
    })
    render(
      <QueryClientProvider client={client}>
        <BillingRatesEditor userId={userId} modelName='client-alias' />
      </QueryClientProvider>
    )
    const user = userEvent.setup()
    const input = await screen.findByRole('textbox', {
      name: 'Rate for server-billing-model',
    })
    expect(input).toHaveValue('0.8')
    expect(get).toHaveBeenCalledWith(
      path,
      expect.objectContaining({ params: { model_name: 'client-alias' } })
    )
    expect(
      screen.queryByRole('textbox', { name: 'Rate for client-alias' })
    ).not.toBeInTheDocument()
    await user.clear(input)
    await user.type(input, '0.6')
    await user.click(screen.getByRole('button', { name: 'Save rates' }))
    await user.click(
      within(screen.getByRole('alertdialog')).getByRole('button', {
        name: 'Save rates',
      })
    )
    await waitFor(() =>
      expect(patch).toHaveBeenCalledWith(
        path,
        {
          changes: [
            {
              model_name: 'server-billing-model',
              rate: '0.6',
              expected_version: 4,
            },
          ],
        },
        { params: { model_name: 'client-alias' } }
      )
    )
  }
)

it('blocks saving until retry resolves the server billing identity, even when public pricing contains the model', async () => {
  useAuthStore.getState().auth.setUser({ id: 1, username: 'root', role: 100 })
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  let resolved = false
  vi.spyOn(api, 'get').mockImplementation(async (url) => {
    if (url === '/api/option/billing_rates') {
      return {
        data: {
          success: true,
          data: {
            rules: [],
            billing_model_name: resolved ? 'server-billing-model' : undefined,
          },
        },
      }
    }
    if (url === '/api/pricing') {
      return {
        data: { success: true, data: [model], vendors: [], group_ratio: {} },
      }
    }
    return { data: { success: true, data: {} } }
  })
  const patch = vi.spyOn(api, 'patch')
  render(
    <QueryClientProvider client={client}>
      <BillingRatesEditor modelName='model-a' />
    </QueryClientProvider>
  )
  expect(
    await screen.findByText('Unable to load billing rates')
  ).toBeInTheDocument()
  expect(
    screen.queryByRole('button', { name: 'Save rates' })
  ).not.toBeInTheDocument()
  expect(screen.queryByRole('textbox')).not.toBeInTheDocument()
  expect(patch).not.toHaveBeenCalled()
  resolved = true
  await userEvent.setup().click(screen.getByRole('button', { name: 'Retry' }))
  expect(
    await screen.findByRole('textbox', {
      name: 'Rate for server-billing-model',
    })
  ).toBeInTheDocument()
})

it('keeps separate server billing identities when multiple model editors are open', async () => {
  useAuthStore.getState().auth.setUser({ id: 1, username: 'root', role: 100 })
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  vi.spyOn(api, 'get').mockImplementation(async (url, options) => {
    if (url === '/api/option/billing_rates') {
      const params = options?.params as { model_name: string }
      return {
        data: {
          success: true,
          data: {
            rules: [],
            billing_model_name: `billing-${params.model_name}`,
          },
        },
      }
    }
    return { data: { success: true, data: [], vendors: [], group_ratio: {} } }
  })
  render(
    <QueryClientProvider client={client}>
      <BillingRatesEditor modelName='first' />
      <BillingRatesEditor modelName='second' />
    </QueryClientProvider>
  )
  expect(
    await screen.findByRole('textbox', { name: 'Rate for billing-first' })
  ).toBeInTheDocument()
  expect(
    await screen.findByRole('textbox', { name: 'Rate for billing-second' })
  ).toBeInTheDocument()
})
