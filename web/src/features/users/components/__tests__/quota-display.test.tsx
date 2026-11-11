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
  RouterProvider,
} from '@tanstack/react-router'
import {
  flexRender,
  getCoreRowModel,
  useReactTable,
} from '@tanstack/react-table'
import {
  act,
  cleanup,
  render,
  screen,
  within,
  waitFor,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { createInstance } from 'i18next'
import { I18nextProvider } from 'react-i18next'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'

import { TooltipProvider } from '@/components/ui/tooltip'
import zh from '@/i18n/locales/zh.json'
import { api } from '@/lib/api'
import { formatTimestampToDate } from '@/lib/format'
import { useAuthStore } from '@/stores/auth-store'
import {
  DEFAULT_CURRENCY_CONFIG,
  useSystemConfigStore,
} from '@/stores/system-config-store'

import { transformUserToFormDefaults } from '../../lib/user-form'
import type { User } from '../../types'
import { UserQuotaDialog } from '../user-quota-dialog'
import { useUsersColumns } from '../users-columns'
import { UsersProvider } from '../users-provider'
import { UsersTable } from '../users-table'

const clients: QueryClient[] = []
const i18n = createInstance()
await i18n.init({
  lng: 'en',
  resources: { en: { translation: {} } },
  initAsync: false,
})

function QuotaTable(props: { remaining: number; used: number }) {
  const columns = useUsersColumns().filter((column) =>
    ['quota', 'used_quota'].includes(
      column.id ?? ('accessorKey' in column ? String(column.accessorKey) : '')
    )
  )
  const table = useReactTable({
    columns,
    data: [
      {
        id: 1,
        username: 'test',
        display_name: '',
        role: 1,
        status: 1,
        quota: props.remaining,
        used_quota: props.used,
        request_count: 0,
        group: 'default',
      } as User,
    ],
    getCoreRowModel: getCoreRowModel(),
  })
  return (
    <table>
      <thead>
        {table.getHeaderGroups().map((group) => (
          <tr key={group.id}>
            {group.headers.map((header) => (
              <th key={header.id} data-sortable={header.column.getCanSort()}>
                {flexRender(
                  header.column.columnDef.header,
                  header.getContext()
                )}
              </th>
            ))}
          </tr>
        ))}
      </thead>
      <tbody>
        {table.getRowModel().rows.map((row) => (
          <tr key={row.id}>
            {row.getVisibleCells().map((cell) => (
              <td key={cell.id}>
                {flexRender(cell.column.columnDef.cell, cell.getContext())}
              </td>
            ))}
          </tr>
        ))}
      </tbody>
    </table>
  )
}

beforeEach(() => {
  vi.spyOn(window, 'scrollTo').mockImplementation(() => {})
  useSystemConfigStore
    .getState()
    .setConfig({ currency: { ...DEFAULT_CURRENCY_CONFIG } })
})
afterEach(() => {
  cleanup()
  clients.splice(0).forEach((client) => client.clear())
  useAuthStore.getState().auth.reset()
  useSystemConfigStore
    .getState()
    .setConfig({ currency: { ...DEFAULT_CURRENCY_CONFIG } })
})

it('shows balance above secondary usage text and opens quota details on click', async () => {
  render(
    <I18nextProvider i18n={i18n}>
      <QuotaTable remaining={1900} used={1100} />
    </I18nextProvider>
  )
  expect(
    screen.getByRole('columnheader', { name: 'Available Balance (USD)' })
  ).toHaveAttribute('data-sortable', 'true')
  expect(screen.getAllByRole('columnheader')).toHaveLength(1)
  const cells = screen.getAllByRole('cell')
  expect(within(cells[0]).getByText('0.0038')).toBeInTheDocument()
  expect(cells).toHaveLength(1)
  expect(
    within(cells[0]).queryByText('Available Balance')
  ).not.toBeInTheDocument()
  expect(within(cells[0]).getByText('0.0022')).toBeInTheDocument()
  expect(screen.getByText('0.0038').parentElement).toHaveClass('grid-cols-1')
  expect(screen.getByText('Used amount').parentElement).toHaveAttribute(
    'data-table-text',
    'secondary'
  )
  expect(screen.queryByRole('progressbar')).not.toBeInTheDocument()
  expect(screen.queryByText('0.006')).not.toBeInTheDocument()
  const trigger = screen.getByRole('button', {
    name: 'Available Balance 0.0038; Used amount 0.0022',
  })
  await userEvent.click(trigger)
  const detail = await screen.findByRole('dialog', { name: 'Quota (USD)' })
  expect(within(detail).getByText('Available Balance')).toBeInTheDocument()
  expect(within(detail).getByText('Total Used')).toBeInTheDocument()
  expect(within(detail).getByText('0.0038')).toBeInTheDocument()
  expect(within(detail).getByText('0.0022')).toBeInTheDocument()
  expect(within(detail).queryByRole('progressbar')).not.toBeInTheDocument()
  await userEvent.keyboard('{Escape}')
  await waitFor(() =>
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  )
  expect(trigger).toHaveFocus()
})

it.each([0, 500000])(
  'shows usage for a zero balance only when used quota is nonzero (used=%s)',
  async (used) => {
    render(
      <I18nextProvider i18n={i18n}>
        <QuotaTable remaining={0} used={used} />
      </I18nextProvider>
    )
    if (used === 0) {
      expect(screen.getAllByRole('cell')[0]).toHaveTextContent(/^No Quota$/)
      expect(screen.queryByText('Used amount')).not.toBeInTheDocument()
      await userEvent.click(screen.getByRole('button', { name: 'No Quota' }))
      const detail = await screen.findByRole('dialog', { name: 'Quota (USD)' })
      expect(within(detail).getAllByText('0')).toHaveLength(2)
      return
    }
    expect(screen.queryByText('No Quota')).not.toBeInTheDocument()
    expect(
      within(screen.getAllByRole('cell')[0]).getByText('0')
    ).toBeInTheDocument()
    expect(
      within(screen.getAllByRole('cell')[0]).getByText('1')
    ).toBeInTheDocument()
  }
)

it('preserves negative balances in details opened with the keyboard', async () => {
  render(
    <I18nextProvider i18n={i18n}>
      <QuotaTable remaining={-500000} used={1000000} />
    </I18nextProvider>
  )
  expect(screen.getByText('-1')).toHaveClass('text-destructive')
  expect(screen.getByText('2')).toBeInTheDocument()
  await userEvent.tab()
  await userEvent.keyboard('{Enter}')
  const detail = await screen.findByRole('dialog')
  expect(within(detail).getByText('-1')).toBeInTheDocument()
  expect(within(detail).getByText('2')).toBeInTheDocument()
})

it('keeps USD balances in USD when custom display is configured', () => {
  useSystemConfigStore.getState().setConfig({
    currency: {
      ...DEFAULT_CURRENCY_CONFIG,
      quotaDisplayType: 'CUSTOM',
      customCurrencySymbol: '🐱',
      customCurrencyExchangeRate: 1,
    },
  })
  render(
    <I18nextProvider i18n={i18n}>
      <QuotaTable remaining={1900} used={1100} />
    </I18nextProvider>
  )
  expect(
    screen.getByRole('columnheader', { name: 'Available Balance (USD)' })
  ).toBeInTheDocument()
  for (const cell of screen.getAllByRole('cell')) {
    expect(cell).not.toHaveTextContent('🐱')
  }
  expect(
    within(screen.getAllByRole('cell')[0]).getByText('0.0038')
  ).toBeInTheDocument()
  expect(
    within(screen.getAllByRole('cell')[0]).getByText('0.0022')
  ).toBeInTheDocument()
})

function UsersPage() {
  return (
    <TooltipProvider>
      <UsersProvider>
        <UsersTable />
      </UsersProvider>
    </TooltipProvider>
  )
}

async function renderUsersList(emptyInvitation = false) {
  useAuthStore.getState().auth.setUser({ id: 1, username: 'admin', role: 100 })
  const usersResponse = {
    data: {
      success: true,
      data: {
        items: [
          {
            id: 2,
            username: 'long-user-name-for-table-layout',
            display_name: 'A display name',
            role: 1,
            status: 1,
            quota: 1900,
            used_quota: 1100,
            request_count: 0,
            created_at: Math.floor(Date.now() / 1000) - 86400,
            last_login_at: Math.floor(Date.now() / 1000) - 20,
            group: 'default',
            aff_count: emptyInvitation ? 0 : 2,
            aff_history_quota: emptyInvitation ? 0 : 500000,
            inviter_id: emptyInvitation ? 0 : 42,
          },
        ],
        total: 1,
      },
    },
  }
  const get = vi
    .spyOn(api, 'get')
    .mockImplementation(async (url) =>
      url === '/api/group/'
        ? { data: { success: true, data: ['default'] } }
        : usersResponse
    )
  const root = createRootRoute()
  const auth = createRoute({ getParentRoute: () => root, id: '_authenticated' })
  const users = createRoute({
    getParentRoute: () => auth,
    path: 'users/',
    component: UsersPage,
  })
  const router = createRouter({
    routeTree: root.addChildren([auth.addChildren([users])]),
    history: createMemoryHistory({ initialEntries: ['/users/'] }),
  })
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  clients.push(client)
  await router.load()
  render(
    <I18nextProvider i18n={i18n}>
      <QueryClientProvider client={client}>
        <RouterProvider router={router} />
      </QueryClientProvider>
    </I18nextProvider>
  )
  await screen.findByText('long-user-name-for-table-layout')
  return get
}

it('sends balance sorting to the server and keeps invitation details on two lines', async () => {
  const get = await renderUsersList()
  expect(
    screen.getByRole('columnheader', { name: 'User Group' })
  ).toBeInTheDocument()
  await userEvent.click(
    screen.getByRole('button', { name: 'Available Balance (USD)' })
  )
  await userEvent.click(screen.getByRole('menuitem', { name: 'Desc' }))
  await waitFor(() =>
    expect(get).toHaveBeenCalledWith('/api/user/', {
      params: expect.objectContaining({ sort_by: 'quota', sort_order: 'desc' }),
    })
  )
  expect(
    screen.queryByRole('button', { name: 'Total Used' })
  ).not.toBeInTheDocument()
  expect(screen.getByText('Inviter ID: 42')).toBeInTheDocument()
  expect(screen.getByText(/Invited 2 users · Earnings:/)).toBeInTheDocument()
})

it('shows balance above usage on mobile cards in Chinese', async () => {
  const originalMatchMedia = window.matchMedia
  vi.spyOn(window, 'matchMedia').mockImplementation((query) => ({
    ...originalMatchMedia(query),
    matches: query.includes('max-width'),
  }))
  i18n.addResourceBundle('zh', 'translation', zh.translation)
  await i18n.changeLanguage('zh')
  try {
    await renderUsersList()
    expect(screen.getByText('可用余额 (USD)')).toBeInTheDocument()
    expect(screen.getByText('已用')).toBeInTheDocument()
    expect(screen.getByText('0.0038')).toBeInTheDocument()
    expect(screen.getByText('0.0022')).toBeInTheDocument()
    expect(screen.queryByRole('table')).not.toBeInTheDocument()
    await userEvent.click(
      screen.getByRole('button', { name: /可用余额 0.0038/ })
    )
    const detail = await screen.findByRole('dialog', { name: '额度 (USD)' })
    expect(within(detail).getByText('累计已用')).toBeInTheDocument()
    expect(within(detail).getByText('0.0022')).toBeInTheDocument()
    await userEvent.keyboard('{Escape}')
  } finally {
    await i18n.changeLanguage('en')
  }
})

it('combines creation and last login into one column with full dates visible directly', async () => {
  vi.spyOn(Date, 'now').mockReturnValue(Date.UTC(2026, 8, 8, 12))
  await renderUsersList(true)
  expect(screen.getByRole('columnheader', { name: /Time/ })).toBeInTheDocument()
  expect(
    screen.queryByRole('columnheader', { name: /Last Login/ })
  ).not.toBeInTheDocument()
  const row = screen.getByRole('row', {
    name: /long-user-name-for-table-layout/,
  })
  expect(within(row).getByText('—')).toBeInTheDocument()
  const times = within(row).getAllByRole('time')
  expect(times).toHaveLength(2)
  expect(times[0]).toHaveTextContent(
    formatTimestampToDate(Math.floor(Date.now() / 1000) - 86400)
  )
  expect(times[1]).toHaveTextContent(
    formatTimestampToDate(Math.floor(Date.now() / 1000) - 20)
  )
  expect(within(row).queryByText('Just now')).not.toBeInTheDocument()
  expect(within(row).getByText('Created')).toBeInTheDocument()
  expect(within(row).getByText('Last Login')).toBeInTheDocument()
  expect(screen.queryByText('No Inviter')).not.toBeInTheDocument()
  await userEvent.click(screen.getByRole('button', { name: 'View' }))
  await userEvent.click(screen.getByRole('menuitemcheckbox', { name: 'Time' }))
  expect(
    screen.queryByRole('columnheader', { name: /Time/ })
  ).not.toBeInTheDocument()
})

it('keeps the USD unit and balances unchanged when the display rate changes', () => {
  render(
    <I18nextProvider i18n={i18n}>
      <QuotaTable remaining={500000} used={1000000} />
    </I18nextProvider>
  )
  act(() =>
    useSystemConfigStore.getState().setConfig({
      currency: {
        ...DEFAULT_CURRENCY_CONFIG,
        quotaDisplayType: 'CNY',
        usdExchangeRate: 7,
      },
    })
  )
  expect(
    screen.getByRole('columnheader', { name: 'Available Balance (USD)' })
  ).toBeInTheDocument()
  expect(
    within(screen.getAllByRole('cell')[0]).getByText('1')
  ).toBeInTheDocument()
  expect(
    within(screen.getAllByRole('cell')[0]).getByText('2')
  ).toBeInTheDocument()
  for (const cell of screen.getAllByRole('cell')) {
    expect(cell).not.toHaveTextContent('¥')
  }
})

it('keeps small USD balances in USD when token display is configured', () => {
  useSystemConfigStore.getState().setConfig({
    currency: { ...DEFAULT_CURRENCY_CONFIG, quotaDisplayType: 'TOKENS' },
  })
  render(
    <I18nextProvider i18n={i18n}>
      <QuotaTable remaining={100} used={200} />
    </I18nextProvider>
  )
  expect(
    screen.getByRole('columnheader', { name: 'Available Balance (USD)' })
  ).toBeInTheDocument()
  expect(
    within(screen.getAllByRole('cell')[0]).getByText('0.0002')
  ).toBeInTheDocument()
  expect(
    within(screen.getAllByRole('cell')[0]).getByText('0.0004')
  ).toBeInTheDocument()
})

function renderQuotaAdjustment() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  clients.push(client)
  const onSuccess = vi.fn()
  render(
    <I18nextProvider i18n={i18n}>
      <QueryClientProvider client={client}>
        <UserQuotaDialog
          open
          userId={42}
          currentQuota={5000000}
          onOpenChange={() => undefined}
          onSuccess={onSuccess}
        />
      </QueryClientProvider>
    </I18nextProvider>
  )
  return { onSuccess }
}

it.each(['CNY', 'CUSTOM', 'TOKENS'] as const)(
  'submits a USD amount unchanged when display mode is %s',
  async (quotaDisplayType) => {
    useSystemConfigStore.getState().setConfig({
      currency: {
        ...DEFAULT_CURRENCY_CONFIG,
        quotaDisplayType,
        usdExchangeRate: 7,
        customCurrencyExchangeRate: 3,
      },
    })
    const post = vi.spyOn(api, 'post').mockResolvedValue({
      data: { success: true },
    })
    expect(
      transformUserToFormDefaults({ quota: 1000000 } as User).quota_dollars
    ).toBe(2)
    renderQuotaAdjustment()
    expect(screen.getByText('Amount (USD)')).toBeVisible()
    await userEvent.type(
      screen.getByRole('spinbutton', { name: 'Amount' }),
      '2'
    )
    await userEvent.click(screen.getByRole('button', { name: 'Confirm' }))
    await waitFor(() =>
      expect(post).toHaveBeenCalledWith('/api/user/manage', {
        id: 42,
        action: 'add_quota',
        mode: 'add',
        value: 1000000,
      })
    )
  }
)

it('supports CNY add, subtract and override without a reason or randomUUID', async () => {
  const wallet = {
    currency: 'CNY',
    quota: 5000000,
    used_quota: 0,
    quota_per_unit: 500000,
  }
  vi.spyOn(api, 'get').mockResolvedValue({
    data: { success: true, data: wallet },
  })
  const post = vi.spyOn(api, 'post').mockResolvedValue({
    data: { success: true, data: wallet },
  })
  const cryptoDescriptor = Object.getOwnPropertyDescriptor(globalThis, 'crypto')
  const getRandomValues = globalThis.crypto.getRandomValues.bind(
    globalThis.crypto
  )
  Object.defineProperty(globalThis, 'crypto', {
    configurable: true,
    value: { getRandomValues },
  })
  try {
    renderQuotaAdjustment()
    await userEvent.click(
      screen.getByRole('combobox', { name: 'Billing currency' })
    )
    await userEvent.click(screen.getByRole('option', { name: 'CNY' }))
    expect(screen.queryByRole('textbox', { name: 'Reason' })).toBeNull()
    for (const [label, mode, amount, value] of [
      ['Add', 'add', '2', 1000000],
      ['Subtract', 'subtract', '1', 500000],
      ['Override', 'override', '0', 0],
      ['Override', 'override', '-1', -500000],
    ] as const) {
      await userEvent.click(screen.getByRole('button', { name: label }))
      await userEvent.type(
        screen.getByRole('spinbutton', { name: 'Amount' }),
        amount
      )
      await userEvent.click(screen.getByRole('button', { name: 'Confirm' }))
      await waitFor(() =>
        expect(post).toHaveBeenLastCalledWith('/api/user/42/currency/CNY', {
          mode,
          value,
          operation_id: expect.stringMatching(/^cny-[A-Za-z0-9_-]+$/),
        })
      )
      await waitFor(() =>
        expect(screen.getByRole('spinbutton', { name: 'Amount' })).toHaveValue(
          null
        )
      )
    }
    const ids = post.mock.calls.map(
      ([, payload]) => (payload as { operation_id: string }).operation_id
    )
    expect(new Set(ids).size).toBe(4)
  } finally {
    if (cryptoDescriptor) {
      Object.defineProperty(globalThis, 'crypto', cryptoDescriptor)
    }
  }
})

it('reuses the CNY operation ID for a failed retry and creates a new one after success', async () => {
  const wallet = {
    currency: 'CNY',
    quota: 5000000,
    used_quota: 0,
    quota_per_unit: 1000000,
  }
  vi.spyOn(api, 'get').mockResolvedValue({
    data: { success: true, data: wallet },
  })
  const post = vi
    .spyOn(api, 'post')
    .mockRejectedValueOnce(new Error('Response lost'))
    .mockResolvedValue({ data: { success: true, data: wallet } })
  const { onSuccess } = renderQuotaAdjustment()
  await userEvent.click(
    screen.getByRole('combobox', { name: 'Billing currency' })
  )
  await userEvent.click(screen.getByRole('option', { name: 'CNY' }))
  await userEvent.type(screen.getByRole('spinbutton', { name: 'Amount' }), '2')
  await userEvent.click(screen.getByRole('button', { name: 'Confirm' }))
  await waitFor(() => expect(post).toHaveBeenCalledTimes(1))
  await waitFor(() =>
    expect(screen.getByRole('button', { name: 'Confirm' })).toBeEnabled()
  )
  expect(screen.getByRole('spinbutton', { name: 'Amount' })).toHaveValue(2)
  await userEvent.click(screen.getByRole('button', { name: 'Confirm' }))
  await waitFor(() => expect(onSuccess).toHaveBeenCalledTimes(1))
  const first = post.mock.calls[0][1] as { operation_id: string }
  expect(first).toMatchObject({ mode: 'add', value: 2000000 })
  expect(post.mock.calls[1][1]).toEqual(first)
  await userEvent.type(screen.getByRole('spinbutton', { name: 'Amount' }), '2')
  await userEvent.click(screen.getByRole('button', { name: 'Confirm' }))
  await waitFor(() => expect(onSuccess).toHaveBeenCalledTimes(2))
  expect(
    (post.mock.calls[2][1] as { operation_id: string }).operation_id
  ).not.toBe(first.operation_id)
})
