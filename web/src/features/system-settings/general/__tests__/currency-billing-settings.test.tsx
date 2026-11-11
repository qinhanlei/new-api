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
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  Outlet,
  RouterProvider,
} from '@tanstack/react-router'
import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import { ROLE } from '@/lib/roles'
import { Route as SystemSettingsRoute } from '@/routes/_authenticated/system-settings/route'
import { useAuthStore } from '@/stores/auth-store'

import { BillingSettings } from '../../billing'

const initialPolicy = {
  cross_currency_enabled: false,
  dynamic_enabled: false,
  static_rate: '7.3',
  interval_hours: 6,
  max_quote_age_hours: 168,
  max_change_percent: '3',
  max_week_change_percent: '10',
  version: 0,
  active_rate_id: 0,
  updated_at: 0,
}
const initialRate = {
  enabled: false,
  rate: '7.3',
  source: 'manual',
  source_time: 0,
  policy_version: 0,
}
const preview = {
  id: 0,
  rate: '6.81',
  source: 'frankfurter',
  source_time: 1794355200,
  observed_at: 0,
  accepted: false,
  reason: '',
  policy_version: 0,
}
const clients: QueryClient[] = []
let settings: {
  policy: typeof initialPolicy
  rate: typeof initialRate
  latest_observation: typeof preview
  status: 'ready' | 'last_good' | 'unavailable'
}

beforeEach(() => {
  vi.spyOn(window, 'scrollTo').mockImplementation(() => undefined)
  settings = {
    policy: { ...initialPolicy },
    rate: { ...initialRate },
    latest_observation: { ...preview, rate: '', source_time: 0 },
    status: 'ready',
  }
  vi.spyOn(api, 'get').mockImplementation(async (url) => {
    if (url === '/api/currency-billing') {
      return { data: { success: true, data: settings } }
    }
    if (url === '/api/currency-billing/preview') {
      return { data: { success: true, data: preview } }
    }
    if (url === '/api/option/') return { data: { success: true, data: [] } }
    throw new Error(`Unexpected GET ${url}`)
  })
  vi.spyOn(api, 'put').mockImplementation(async (url, body) => {
    if (url === '/api/currency-billing') {
      const request = body as { policy: typeof initialPolicy }
      settings = {
        ...settings,
        policy: { ...request.policy, version: request.policy.version + 1 },
      }
      return { data: { success: true, data: settings } }
    }
    return { data: { success: true } }
  })
})

afterEach(() => {
  cleanup()
  clients.splice(0).forEach((client) => client.clear())
  useAuthStore.getState().auth.reset()
})

async function renderSettings(
  role: number = ROLE.ADMIN,
  path = '/system-settings/billing/currency'
) {
  useAuthStore.getState().auth.setUser({ id: 1, username: 'admin', role })
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  clients.push(client)
  const root = createRootRoute({ component: Outlet })
  const authenticated = createRoute({
    getParentRoute: () => root,
    id: '_authenticated',
    component: Outlet,
  })
  const guard = SystemSettingsRoute.options.beforeLoad
  const system = createRoute({
    getParentRoute: () => authenticated,
    path: 'system-settings',
    beforeLoad: (context) =>
      guard?.(context as unknown as Parameters<NonNullable<typeof guard>>[0]),
    component: Outlet,
  })
  const billing = createRoute({
    getParentRoute: () => system,
    path: 'billing/$section',
    component: BillingSettings,
  })
  const site = createRoute({
    getParentRoute: () => system,
    path: 'site',
    component: () => <p>Root site settings</p>,
  })
  const forbidden = createRoute({
    getParentRoute: () => root,
    path: '403',
    component: () => <p>Forbidden settings</p>,
  })
  const router = createRouter({
    routeTree: root.addChildren([
      authenticated.addChildren([system.addChildren([billing, site])]),
      forbidden,
    ]),
    history: createMemoryHistory({ initialEntries: [path] }),
  })
  await router.load()
  render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  )
  return { client, router }
}

it('lets an admin save static wallet currency settings without loading or changing root options', async () => {
  await renderSettings()
  const cross = await screen.findByRole('switch', {
    name: 'Cross-currency wallet payment',
  })
  expect(cross).not.toBeChecked()
  expect(
    screen.getByRole('switch', { name: 'Dynamic exchange rate' })
  ).not.toBeChecked()
  expect(
    screen.queryByRole('combobox', { name: 'Display Mode' })
  ).not.toBeInTheDocument()
  await userEvent.click(cross)
  fireEvent.change(
    screen.getByRole('spinbutton', {
      name: 'Wallet exchange rate (CNY per USD)',
    }),
    { target: { value: '6.7' } }
  )
  await userEvent.click(screen.getByRole('button', { name: 'Save Changes' }))
  await waitFor(() =>
    expect(api.put).toHaveBeenCalledWith('/api/currency-billing', {
      policy: {
        ...initialPolicy,
        cross_currency_enabled: true,
        static_rate: '6.7',
      },
    })
  )
  expect(
    vi.mocked(api.get).mock.calls.some(([url]) => url === '/api/option/')
  ).toBe(false)
  expect(
    vi.mocked(api.put).mock.calls.some(([url]) => url === '/api/option/')
  ).toBe(false)
  expect(
    vi.mocked(api.get).mock.calls.some(([url]) => url.endsWith('/preview'))
  ).toBe(false)
})

it('previews the first dynamic rate and preserves the draft after a failed save', async () => {
  await renderSettings()
  await userEvent.click(
    await screen.findByRole('switch', { name: 'Dynamic exchange rate' })
  )
  const baseline = await screen.findByRole('status', {
    name: 'Initial dynamic exchange rate',
  })
  expect(baseline).toHaveTextContent('1 USD = 6.81 CNY')
  expect(baseline).toHaveTextContent('Frankfurter')
  fireEvent.change(
    screen.getByRole('spinbutton', { name: 'Update interval (hours)' }),
    { target: { value: '12' } }
  )
  await userEvent.click(
    screen.getByRole('switch', { name: 'Dynamic exchange rate' })
  )
  await userEvent.click(
    screen.getByRole('switch', { name: 'Dynamic exchange rate' })
  )
  expect(
    screen.getByRole('spinbutton', { name: 'Update interval (hours)' })
  ).toHaveValue(12)
  await waitFor(() =>
    expect(screen.getByRole('button', { name: 'Save Changes' })).toBeEnabled()
  )
  vi.mocked(api.put).mockResolvedValueOnce({
    data: {
      success: false,
      message: 'Exchange rate preview changed; reload before saving',
    },
  })
  await userEvent.click(screen.getByRole('button', { name: 'Save Changes' }))
  expect(await screen.findByRole('alert')).toHaveTextContent(
    'Exchange rate preview changed; reload before saving'
  )
  expect(
    screen.getByRole('spinbutton', { name: 'Update interval (hours)' })
  ).toHaveValue(12)
  await userEvent.click(
    screen.getByRole('button', { name: 'Refresh rate preview' })
  )
  await waitFor(() =>
    expect(screen.getByRole('button', { name: 'Save Changes' })).toBeEnabled()
  )
  await userEvent.click(screen.getByRole('button', { name: 'Save Changes' }))
  await waitFor(() => expect(api.put).toHaveBeenCalledTimes(2))
  expect(vi.mocked(api.put).mock.lastCall).toEqual([
    '/api/currency-billing',
    {
      policy: { ...initialPolicy, dynamic_enabled: true, interval_hours: 12 },
      baseline_rate: '6.81',
    },
  ])
})

it('blocks dynamic saves until a failed preview is successfully retried', async () => {
  const get = vi.mocked(api.get).getMockImplementation()
  if (!get) throw new Error('API fixture is not initialized')
  vi.mocked(api.get).mockImplementation(async (url, config) => {
    if (url.endsWith('/preview')) {
      throw new Error('Failed to fetch exchange rate')
    }
    return get(url, config)
  })
  await renderSettings()
  await userEvent.click(
    await screen.findByRole('switch', { name: 'Dynamic exchange rate' })
  )
  expect(await screen.findByText('Failed to fetch exchange rate')).toBeVisible()
  expect(screen.getByRole('button', { name: 'Save Changes' })).toBeDisabled()
  vi.mocked(api.get).mockImplementation(get)
  await userEvent.click(
    screen.getByRole('button', { name: 'Refresh rate preview' })
  )
  expect(
    await screen.findByRole('status', { name: 'Initial dynamic exchange rate' })
  ).toHaveTextContent('6.81')
  await waitFor(() =>
    expect(screen.getByRole('button', { name: 'Save Changes' })).toBeEnabled()
  )
})

it.each([
  ['Update interval (hours)', '0'],
  ['Maximum rate age (hours)', '5'],
  ['Maximum daily change (%)', '0'],
  ['Maximum weekly change (%)', '51'],
])(
  'rejects invalid %s before sending wallet settings',
  async (label, value) => {
    await renderSettings()
    await userEvent.click(
      await screen.findByRole('switch', { name: 'Dynamic exchange rate' })
    )
    await screen.findByRole('status', { name: 'Initial dynamic exchange rate' })
    await userEvent.click(
      screen.getByRole('button', { name: 'Exchange rate safeguards' })
    )
    const input = screen.getByRole('spinbutton', { name: label })
    fireEvent.change(input, { target: { value } })
    await userEvent.click(screen.getByRole('button', { name: 'Save Changes' }))
    await waitFor(() => expect(input).toHaveAttribute('aria-invalid', 'true'))
    expect(api.put).not.toHaveBeenCalled()
  }
)

it.each(['last_good', 'unavailable'] as const)(
  'shows the %s rate state and the rejected observation',
  async (status) => {
    settings = {
      ...settings,
      policy: {
        ...initialPolicy,
        cross_currency_enabled: true,
        dynamic_enabled: true,
      },
      status,
      latest_observation: {
        ...preview,
        id: 7,
        observed_at: 1794398400,
        reason: 'Exchange rate change exceeds 3%',
      },
    }
    await renderSettings()
    expect(
      await screen.findByRole('status', { name: 'Exchange rate status' })
    ).toHaveTextContent(
      status === 'last_good'
        ? 'Using last accepted rate'
        : 'Cross-currency payment unavailable'
    )
    expect(screen.getByText('Exchange rate change exceeds 3%')).toBeVisible()
  }
)

it.each([
  [ROLE.USER, '/system-settings/billing/currency'],
  [ROLE.ADMIN, '/system-settings/billing/payment'],
  [ROLE.ADMIN, '/system-settings/billing/currency-other'],
  [ROLE.ADMIN, '/system-settings/site'],
])(
  'denies role %s access to %s before fetching settings',
  async (role, path) => {
    await renderSettings(role, path)
    expect(await screen.findByText('Forbidden settings')).toBeVisible()
    expect(api.get).not.toHaveBeenCalled()
  }
)

it('preserves root access to other system settings pages', async () => {
  await renderSettings(ROLE.SUPER_ADMIN, '/system-settings/site')
  expect(await screen.findByText('Root site settings')).toBeVisible()
})

it('rechecks admin access when leaving the currency page for another settings page', async () => {
  const { router } = await renderSettings()
  await screen.findByRole('switch', { name: 'Cross-currency wallet payment' })
  await act(() =>
    router.navigate({
      to: '/system-settings/billing/$section',
      params: { section: 'payment' },
    })
  )
  expect(await screen.findByText('Forbidden settings')).toBeVisible()
  expect(
    vi.mocked(api.get).mock.calls.some(([url]) => url === '/api/option/')
  ).toBe(false)
})

it('keeps root display settings separate from wallet currency saves', async () => {
  await renderSettings(ROLE.SUPER_ADMIN)
  const wallet = await screen.findByRole('region', {
    name: 'Wallet currency billing',
  })
  expect(
    await screen.findByRole('combobox', { name: 'Display Mode' })
  ).toBeVisible()
  const displayForm = screen.getByRole('form', { name: 'Pricing & Display' })
  const paymentRate = within(displayForm).getByRole('spinbutton', {
    name: 'USD Exchange Rate',
  })
  fireEvent.change(paymentRate, { target: { value: '9' } })
  await userEvent.click(
    within(wallet).getByRole('switch', {
      name: 'Cross-currency wallet payment',
    })
  )
  await userEvent.click(
    within(wallet).getByRole('button', { name: 'Save Changes' })
  )
  await waitFor(() => expect(api.put).toHaveBeenCalledTimes(1))
  expect(vi.mocked(api.put).mock.lastCall?.[0]).toBe('/api/currency-billing')
  expect(paymentRate).toHaveValue(9)
  await userEvent.click(
    within(displayForm).getByRole('button', { name: 'Save Changes' })
  )
  await waitFor(() =>
    expect(api.put).toHaveBeenCalledWith('/api/option/', {
      key: 'USDExchangeRate',
      value: '9',
    })
  )
  expect(screen.getByRole('combobox', { name: 'Display Mode' })).toBeVisible()
})

it('retains edits on a policy conflict and only reloads saved values when requested', async () => {
  await renderSettings()
  const input = await screen.findByRole('spinbutton', {
    name: 'Wallet exchange rate (CNY per USD)',
  })
  fireEvent.change(input, { target: { value: '6.7' } })
  settings = {
    ...settings,
    policy: { ...initialPolicy, version: 2, static_rate: '6.9' },
  }
  vi.mocked(api.put).mockResolvedValueOnce({
    data: {
      success: false,
      message: 'Currency settings have changed; reload before saving',
    },
  })
  await userEvent.click(screen.getByRole('button', { name: 'Save Changes' }))
  expect(await screen.findByRole('alert')).toHaveTextContent(
    'Currency settings have changed; reload before saving'
  )
  expect(input).toHaveValue(6.7)
  await userEvent.click(screen.getByRole('button', { name: 'Reload settings' }))
  await waitFor(() => expect(input).toHaveValue(6.9))
  expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  fireEvent.change(input, { target: { value: '6.8' } })
  await userEvent.click(screen.getByRole('button', { name: 'Save Changes' }))
  await waitFor(() =>
    expect(api.put).toHaveBeenCalledWith('/api/currency-billing', {
      policy: { ...initialPolicy, version: 2, static_rate: '6.8' },
    })
  )
})

it('allows correcting an invalid existing static rate instead of crashing the form', async () => {
  settings = {
    ...settings,
    policy: { ...initialPolicy, static_rate: '0.001' },
    status: 'unavailable',
  }
  await renderSettings()
  const input = await screen.findByRole('spinbutton', {
    name: 'Wallet exchange rate (CNY per USD)',
  })
  fireEvent.change(input, { target: { value: '0' } })
  await userEvent.click(screen.getByRole('button', { name: 'Save Changes' }))
  await waitFor(() => expect(input).toHaveAttribute('aria-invalid', 'true'))
  expect(api.put).not.toHaveBeenCalled()
  fireEvent.change(input, { target: { value: '7.3' } })
  await userEvent.click(screen.getByRole('button', { name: 'Save Changes' }))
  await waitFor(() => expect(api.put).toHaveBeenCalledTimes(1))
})
