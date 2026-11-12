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
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, test, vi } from 'vitest'

import type { ApiKey, CNYTokenBudget } from '../../types'

const { createInstance } = await import('i18next')
const { I18nextProvider, initReactI18next } = await import('react-i18next')
const { QueryClient, QueryClientProvider } =
  await import('@tanstack/react-query')
const { api } = await import('@/lib/api')
const { toast } = await import('sonner')
const { ApiKeysProvider } = await import('../api-keys-provider')
const { ApiKeysMutateDrawer } = await import('../api-keys-mutate-drawer')

const i18n = createInstance()
await i18n.use(initReactI18next).init({
  lng: 'en',
  resources: { en: { translation: {} } },
})

type ApiMethod = (url: string, data?: unknown) => Promise<{ data: unknown }>
type MockableApi = {
  get: ApiMethod
  post: ApiMethod
}
type RenderedDrawer = {
  queryClient: InstanceType<typeof QueryClient>
}

const apiClient = api as unknown as MockableApi
const originalGet = apiClient.get
const originalPost = apiClient.post
let renderedDrawer: RenderedDrawer | null = null

function installApiFixtures(createdPayloads: Array<Record<string, unknown>>) {
  apiClient.get = async (url) => {
    switch (url) {
      case '/api/status':
        return { data: { data: { default_use_auto_group: true } } }
      case '/api/user/models':
        return { data: { success: true, data: [] } }
      case '/api/user/self/groups':
        return {
          data: {
            success: true,
            data: {
              auto: { desc: 'Automatic routing', ratio: 'auto' },
              default: { desc: 'Standard access', ratio: 1 },
              vip: { desc: 'Priority access', ratio: 2 },
            },
          },
        }
      case '/api/token/auto-groups':
        return {
          data: {
            success: true,
            data: { groups: ['vip', 'default'], max_count: 3 },
          },
        }
      default:
        if (/^\/api\/token\/\d+\/currency\/CNY$/.test(url)) {
          return {
            data: {
              success: true,
              data: {
                enabled: false,
                unlimited_quota: false,
                remain_quota: 0,
                used_quota: 0,
                version: 0,
                quota_per_unit: 500000,
              },
            },
          }
        }
        throw new Error(`Unexpected GET ${url}`)
    }
  }
  apiClient.post = async (url, data) => {
    expect(url).toBe('/api/token/')
    expect(data && typeof data === 'object').toBeTruthy()
    createdPayloads.push(data as Record<string, unknown>)
    return { data: { success: true, data: {} } }
  }
}

async function renderCreateDrawer(
  onOpenChange = (_open: boolean) => undefined
): Promise<void> {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  const freshAt = Date.now() + 60_000
  queryClient.setQueryData(
    ['status'],
    { default_use_auto_group: true },
    { updatedAt: freshAt }
  )
  queryClient.setQueryData(
    ['user-models'],
    { success: true, data: [] },
    { updatedAt: freshAt }
  )
  queryClient.setQueryData(
    ['user-groups'],
    {
      success: true,
      data: {
        auto: { desc: 'Automatic routing', ratio: 'auto' },
        default: { desc: 'Standard access', ratio: 1 },
        vip: { desc: 'Priority access', ratio: 2 },
      },
    },
    { updatedAt: freshAt }
  )
  queryClient.setQueryData(
    ['token-auto-groups'],
    {
      success: true,
      data: { groups: ['vip', 'default'], max_count: 3 },
    },
    { updatedAt: freshAt }
  )
  renderedDrawer = { queryClient }

  render(
    <QueryClientProvider client={queryClient}>
      <I18nextProvider i18n={i18n}>
        <ApiKeysProvider>
          <ApiKeysMutateDrawer open onOpenChange={onOpenChange} />
        </ApiKeysProvider>
      </I18nextProvider>
    </QueryClientProvider>
  )
  await waitFor(
    () => {
      const saveButton = findButton('Save changes', false)
      expect(saveButton).toBeEnabled()
    },
    { timeout: 1500 }
  )
}

function findButton(text: string, required: true): HTMLButtonElement
function findButton(text: string, required: false): HTMLButtonElement | null
function findButton(text: string, required = true): HTMLButtonElement | null {
  const button = screen
    .queryAllByRole<HTMLButtonElement>('button')
    .find((candidate) => candidate.textContent?.includes(text))
  if (required && !button) {
    throw new Error(`Expected button containing "${text}"`)
  }
  return button ?? null
}

function getControlByLabel(labelText: 'Name' | 'Quantity'): HTMLInputElement
function getControlByLabel(labelText: 'Group'): HTMLButtonElement
function getControlByLabel(labelText: 'Auto group order'): HTMLElement
function getControlByLabel(labelText: string): HTMLElement {
  const label = [...document.querySelectorAll<HTMLLabelElement>('label')].find(
    (candidate) => candidate.textContent?.trim() === labelText
  )
  if (!label) {
    throw new Error(`Expected label "${labelText}"`)
  }

  const control =
    label.control ??
    label
      .closest('[data-slot="form-item"]')
      ?.querySelector<HTMLElement>(
        '[data-slot="form-control"], input, textarea, button[role="combobox"], [role="group"]'
      )
  if (!control) {
    throw new Error(`Expected control for label "${labelText}"`)
  }
  return control
}

function changeInput(input: HTMLInputElement, value: string): void {
  fireEvent.input(input, { target: { value } })
}

function selectComboboxOption(
  trigger: HTMLButtonElement,
  optionDescription: string
): void {
  fireEvent.click(trigger)
  const option = [
    ...document.querySelectorAll<HTMLElement>('[data-slot="command-item"]'),
  ].find((candidate) => candidate.textContent?.includes(optionDescription))
  if (!option) {
    throw new Error(`Expected option containing "${optionDescription}"`)
  }
  fireEvent.click(option)
}

afterEach(() => {
  vi.restoreAllMocks()
  apiClient.get = originalGet
  apiClient.post = originalPost
  localStorage.clear()
  if (renderedDrawer) {
    renderedDrawer.queryClient.clear()
    renderedDrawer = null
  }
})

function renderCurrencyEditDrawer(budgets: Array<CNYTokenBudget | Error>) {
  installApiFixtures([])
  const currentRow: ApiKey = {
    id: 42,
    name: 'currency-key',
    key: '',
    status: 1,
    remain_quota: 0,
    used_quota: 0,
    unlimited_quota: true,
    expired_time: -1,
    created_time: 1,
    accessed_time: 0,
    group: 'default',
    auto_groups: [],
    cross_group_retry: false,
    model_limits_enabled: false,
    model_limits: '',
    allow_ips: '',
    allow_user_quota_query: true,
  }
  const getFixture = apiClient.get
  let budgetIndex = 0
  vi.spyOn(apiClient, 'get').mockImplementation(async (url) => {
    if (url === '/api/token/42') {
      return { data: { success: true, data: currentRow } }
    }
    if (url === '/api/token/42/currency/CNY') {
      const budget = budgets[Math.min(budgetIndex++, budgets.length - 1)]
      if (budget instanceof Error) throw budget
      return { data: { success: true, data: budget } }
    }
    return getFixture(url)
  })
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  renderedDrawer = { queryClient }
  const close = vi.fn()
  render(
    <QueryClientProvider client={queryClient}>
      <I18nextProvider i18n={i18n}>
        <ApiKeysProvider>
          <ApiKeysMutateDrawer
            open
            currentRow={currentRow}
            onOpenChange={close}
          />
        </ApiKeysProvider>
      </I18nextProvider>
    </QueryClientProvider>
  )
  return close
}

const limitedCnyBudget: CNYTokenBudget = {
  enabled: true,
  unlimited_quota: false,
  remain_quota: 500000,
  used_quota: 250000,
  version: 4,
  quota_per_unit: 500000,
}

describe('API keys mutate drawer Auto group integration', () => {
  test('inherits the root Auto order and sends an empty override for every batch-created key', async () => {
    const createdPayloads: Array<Record<string, unknown>> = []
    installApiFixtures(createdPayloads)
    await renderCreateDrawer()

    const groupTrigger = getControlByLabel('Group')
    expect(groupTrigger.textContent?.includes('auto')).toBe(true)
    expect(
      document.body.textContent?.includes(
        'Using the complete global Auto order (2 groups)'
      )
    ).toBe(true)
    expect(
      [
        ...document.querySelectorAll('[data-slot="global-auto-order-name"]'),
      ].map((item) => item.textContent)
    ).toEqual(['vip', 'default'])
    expect(findButton('Restore global Auto', true).disabled).toBe(true)

    changeInput(getControlByLabel('Name'), 'batch')
    changeInput(getControlByLabel('Quantity'), '2')
    fireEvent.click(findButton('Save changes', true))
    await waitFor(() => expect(createdPayloads).toHaveLength(2))

    expect(createdPayloads.length).toBe(2)
    expect(createdPayloads[0]?.name).toBe('batch')
    for (const payload of createdPayloads) {
      expect(payload.group).toBe('auto')
      expect(payload.auto_groups).toEqual([])
      expect(payload.cross_group_retry).toBe(true)
    }
  })

  test('preserves an unsaved custom order and mode after Auto to ordinary to Auto changes', async () => {
    const createdPayloads: Array<Record<string, unknown>> = []
    installApiFixtures(createdPayloads)
    await renderCreateDrawer()

    const autoOrderControl = getControlByLabel('Auto group order')
    const addGroupTrigger = autoOrderControl.querySelector<HTMLButtonElement>(
      'button[role="combobox"]'
    )
    if (!addGroupTrigger) {
      throw new Error('Expected Auto group order combobox')
    }
    selectComboboxOption(addGroupTrigger, 'Priority access')

    expect(
      document.querySelector('button[aria-label="Remove vip"]')
    ).toBeTruthy()
    expect(document.body.textContent?.includes('1 / 3 groups selected')).toBe(
      true
    )
    expect(findButton('Restore global Auto', true).disabled).toBe(false)

    const groupTrigger = getControlByLabel('Group')
    selectComboboxOption(groupTrigger, 'Standard access')
    expect(document.querySelector('button[aria-label="Remove vip"]')).toBe(null)
    selectComboboxOption(groupTrigger, 'Automatic routing')

    expect(
      document.querySelector('button[aria-label="Remove vip"]')
    ).toBeTruthy()
    expect(document.body.textContent?.includes('1 / 3 groups selected')).toBe(
      true
    )
    expect(findButton('Restore global Auto', true).disabled).toBe(false)

    changeInput(getControlByLabel('Name'), 'custom')
    fireEvent.click(findButton('Save changes', true))
    await waitFor(() => expect(createdPayloads).toHaveLength(1))
    expect(createdPayloads[0]?.auto_groups).toEqual(['vip'])
  })
})

describe('API key expiry across editing sessions', () => {
  test.each(['close', 'save'] as const)(
    'keeps an uncached key non-expiring after %s of a key with an expiry',
    async (finish) => {
      installApiFixtures([])
      const first: ApiKey = {
        id: 1,
        name: 'expiring',
        key: '',
        status: 1,
        remain_quota: 0,
        used_quota: 0,
        unlimited_quota: true,
        expired_time: 2000000000,
        created_time: 1,
        accessed_time: 0,
        group: 'default',
        auto_groups: [],
        cross_group_retry: false,
        model_limits_enabled: false,
        model_limits: '',
        allow_ips: '',
        allow_user_quota_query: true,
      }
      const second = { ...first, id: 2, name: 'non-expiring', expired_time: -1 }
      let resolveDetail!: (value: { data: unknown }) => void
      const detail = new Promise<{ data: unknown }>((resolve) => {
        resolveDetail = resolve
      })
      const getFixture = apiClient.get
      const get = vi.spyOn(apiClient, 'get').mockImplementation((url) => {
        if (url === '/api/token/1') {
          return Promise.resolve({ data: { success: true, data: first } })
        }
        if (url === '/api/token/2') return detail
        return getFixture(url)
      })
      const put = vi
        .spyOn(api, 'put')
        .mockResolvedValue({ data: { success: true } })
      const queryClient = new QueryClient({
        defaultOptions: { queries: { retry: false } },
      })
      renderedDrawer = { queryClient }
      const close = vi.fn()
      const tree = (currentRow: ApiKey, open: boolean) => (
        <QueryClientProvider client={queryClient}>
          <I18nextProvider i18n={i18n}>
            <ApiKeysProvider>
              <ApiKeysMutateDrawer
                open={open}
                currentRow={currentRow}
                onOpenChange={close}
              />
            </ApiKeysProvider>
          </I18nextProvider>
        </QueryClientProvider>
      )
      const view = render(tree(first, true))
      await waitFor(() =>
        expect(findButton('Save changes', true)).toBeEnabled()
      )
      await waitFor(() => expect(screen.getByLabelText('Time')).toBeEnabled())

      if (finish === 'save') {
        fireEvent.click(findButton('Save changes', true))
        await waitFor(() => expect(put).toHaveBeenCalled())
        expect(put.mock.calls[0][1]).toMatchObject({
          id: 1,
          expired_time: 2000000000,
        })
      } else {
        fireEvent.click(screen.getAllByRole('button', { name: 'Close' })[0])
      }
      await waitFor(() => expect(close.mock.calls[0]?.[0]).toBe(false))
      view.rerender(tree(first, false))
      await waitFor(() =>
        expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
      )
      view.rerender(tree(second, true))
      await waitFor(() => expect(get).toHaveBeenCalledWith('/api/token/2'))
      expect(findButton('Save changes', true)).toBeDisabled()
      await act(async () =>
        resolveDetail({ data: { success: true, data: second } })
      )
      await waitFor(() =>
        expect(findButton('Save changes', true)).toBeEnabled()
      )
      expect(
        screen.getByRole('button', { name: 'Never expires' })
      ).toBeVisible()
      expect(screen.getByLabelText('Time')).toBeDisabled()
      fireEvent.click(findButton('Save changes', true))
      await waitFor(() =>
        expect(put).toHaveBeenLastCalledWith(
          '/api/token/',
          expect.objectContaining({ id: 2, expired_time: -1 })
        )
      )
    }
  )

  test.each(['Never', 'Clear'])(
    'submits a non-expiring key when its date is cleared with %s',
    async (action) => {
      const payloads: Array<Record<string, unknown>> = []
      installApiFixtures(payloads)
      await renderCreateDrawer()
      changeInput(getControlByLabel('Name'), 'no-expiry')
      fireEvent.click(screen.getByRole('button', { name: '1 Month' }))
      expect(screen.getByLabelText('Time')).toBeEnabled()
      fireEvent.click(screen.getByRole('button', { name: action }))
      expect(
        screen.getByRole('button', { name: 'Never expires' })
      ).toBeVisible()
      expect(screen.getByLabelText('Time')).toBeDisabled()
      fireEvent.click(findButton('Save changes', true))
      await waitFor(() => expect(payloads).toHaveLength(1))
      expect(payloads[0]?.expired_time).toBe(-1)
    }
  )
})

describe('API keys mutate drawer user quota query switch', () => {
  test('sends false for a new key after the switch, which starts on, is turned off', async () => {
    const createdPayloads: Array<Record<string, unknown>> = []
    installApiFixtures(createdPayloads)
    await renderCreateDrawer()

    fireEvent.click(findButton('Advanced Settings', true))
    const quotaQuerySwitch = await screen.findByRole('switch', {
      name: 'Allow querying user quota',
    })
    expect(quotaQuerySwitch).toBeChecked()
    fireEvent.click(quotaQuerySwitch)
    expect(quotaQuerySwitch).not.toBeChecked()

    changeInput(getControlByLabel('Name'), 'no-user-quota')
    fireEvent.click(findButton('Save changes', true))
    await waitFor(() => expect(createdPayloads).toHaveLength(1))
    expect(createdPayloads[0]?.allow_user_quota_query).toBe(false)
  })
})

describe('API key currency controls', () => {
  test('rejects a newly entered negative CNY budget without sending a request', async () => {
    renderCurrencyEditDrawer([limitedCnyBudget])
    const put = vi.spyOn(api, 'put')
    await waitFor(() => expect(findButton('Save changes', true)).toBeEnabled())
    fireEvent.change(
      screen.getByRole('spinbutton', { name: 'Remaining CNY quota' }),
      { target: { value: '-1' } }
    )
    fireEvent.click(findButton('Save changes', true))
    expect(await screen.findByRole('alert')).toHaveTextContent(
      'Quota must be zero or greater'
    )
    expect(put).not.toHaveBeenCalled()
  })

  test('keeps ordinary and CNY drafts after a failed save and reloads only the CNY balance', async () => {
    const close = renderCurrencyEditDrawer([
      limitedCnyBudget,
      { ...limitedCnyBudget, remain_quota: 250000, version: 5 },
    ])
    const put = vi
      .spyOn(api, 'put')
      .mockResolvedValueOnce({
        data: {
          success: false,
          message: 'CNY token budget has changed; reload before saving',
        },
      })
      .mockResolvedValue({ data: { success: true } })
    await waitFor(() => expect(findButton('Save changes', true)).toBeEnabled())
    fireEvent.change(getControlByLabel('Name'), {
      target: { value: 'unsaved-name' },
    })
    fireEvent.change(
      screen.getByRole('spinbutton', { name: 'Remaining CNY quota' }),
      { target: { value: '4' } }
    )
    fireEvent.click(findButton('Save changes', true))
    await screen.findByRole('alert')
    expect(close).not.toHaveBeenCalled()
    expect(getControlByLabel('Name')).toHaveValue('unsaved-name')
    expect(
      screen.getByRole('spinbutton', { name: 'Remaining CNY quota' })
    ).toHaveValue(4)
    expect(findButton('Save changes', true)).toBeDisabled()
    fireEvent.click(screen.getByRole('button', { name: 'Reload balance' }))
    await waitFor(() => expect(findButton('Save changes', true)).toBeEnabled())
    expect(getControlByLabel('Name')).toHaveValue('unsaved-name')
    expect(
      screen.getByRole('spinbutton', { name: 'Remaining CNY quota' })
    ).toHaveValue(0.5)
    fireEvent.change(
      screen.getByRole('spinbutton', { name: 'Remaining CNY quota' }),
      { target: { value: '2' } }
    )
    fireEvent.click(findButton('Save changes', true))
    await waitFor(() => expect(close).toHaveBeenCalledWith(false))
    expect(put).toHaveBeenLastCalledWith(
      '/api/token/',
      expect.objectContaining({
        name: 'unsaved-name',
        cny_budget: {
          enabled: true,
          unlimited_quota: false,
          remain_quota: 1000000,
          expected_version: 5,
        },
      })
    )
  })

  test('blocks saving when the CNY budget cannot load and keeps retry accessible', async () => {
    renderCurrencyEditDrawer([new Error('offline'), limitedCnyBudget])
    const retry = await screen.findByRole('button', { name: 'Retry' })
    await waitFor(() => expect(retry.closest('[inert]')).toBeNull())
    expect(findButton('Save changes', true)).toBeDisabled()
    fireEvent.change(getControlByLabel('Name'), {
      target: { value: 'draft-while-offline' },
    })
    fireEvent.click(retry)
    await waitFor(() => expect(findButton('Save changes', true)).toBeEnabled())
    expect(getControlByLabel('Name')).toHaveValue('draft-while-offline')
    expect(
      screen.getByRole('spinbutton', { name: 'Remaining CNY quota' })
    ).toHaveValue(1)
  })

  test.each([0, -500000])(
    'does not submit an unchanged legacy CNY budget with balance %s and ordinary fields',
    async (remainQuota) => {
      renderCurrencyEditDrawer([
        {
          ...limitedCnyBudget,
          enabled: false,
          version: 0,
          remain_quota: remainQuota,
        },
      ])
      const put = vi
        .spyOn(api, 'put')
        .mockResolvedValue({ data: { success: true } })
      await waitFor(() =>
        expect(findButton('Save changes', true)).toBeEnabled()
      )
      expect(
        screen.getByRole('switch', { name: 'Enable CNY billing' })
      ).not.toBeChecked()
      fireEvent.change(getControlByLabel('Name'), {
        target: { value: 'renamed' },
      })
      fireEvent.click(findButton('Save changes', true))
      await waitFor(() => expect(put).toHaveBeenCalled())
      expect(put.mock.calls[0][1]).not.toHaveProperty('cny_budget')
    }
  )

  test.each(['business', 'network'] as const)(
    'reports completed keys when a batch stops on a %s failure',
    async (failure) => {
      const payloads: Array<Record<string, unknown>> = []
      installApiFixtures(payloads)
      const postFixture = apiClient.post
      const post = vi
        .spyOn(apiClient, 'post')
        .mockImplementation(async (url, payload) => {
          if (payloads.length > 0) {
            if (failure === 'network') throw new Error('offline')
            return { data: { success: false, message: 'limit reached' } }
          }
          return postFixture(url, payload)
        })
      const success = vi.spyOn(toast, 'success')
      const close = vi.fn()
      await renderCreateDrawer(close)
      changeInput(getControlByLabel('Name'), 'limited-batch')
      changeInput(getControlByLabel('Quantity'), '3')
      fireEvent.click(
        screen.getByRole('switch', { name: 'Enable CNY billing' })
      )
      fireEvent.click(findButton('Save changes', true))
      await waitFor(() => expect(close).toHaveBeenCalledWith(false))
      expect(post).toHaveBeenCalledTimes(2)
      expect(success).toHaveBeenCalledWith('Successfully created 1 API Key(s)')
      expect(payloads[0].cny_budget).toEqual({
        enabled: false,
        unlimited_quota: true,
        remain_quota: 0,
        expected_version: 0,
      })
    }
  )

  test('creates a key with CNY enabled and unlimited through the main save', async () => {
    const payloads: Array<Record<string, unknown>> = []
    installApiFixtures(payloads)
    await renderCreateDrawer()
    const unlimited = screen.getByRole('switch', { name: 'Unlimited Quota' })
    const cnyEnabled = screen.getByRole('switch', {
      name: 'Enable CNY billing',
    })
    expect(cnyEnabled).toBeChecked()
    expect(
      screen.getByRole('switch', { name: 'Unlimited CNY quota for this key' })
    ).toBeChecked()
    expect(unlimited.compareDocumentPosition(cnyEnabled)).toBe(
      Node.DOCUMENT_POSITION_FOLLOWING
    )
    fireEvent.click(unlimited)
    const amount = screen.getByRole('spinbutton', { name: 'Quota (USD)' })
    expect(amount.compareDocumentPosition(unlimited)).toBe(
      Node.DOCUMENT_POSITION_FOLLOWING
    )
    changeInput(getControlByLabel('Name'), 'cny-default')
    fireEvent.click(findButton('Save changes', true))
    await waitFor(() => expect(payloads).toHaveLength(1))
    expect(payloads[0].cny_budget).toEqual({
      enabled: true,
      unlimited_quota: true,
      remain_quota: 0,
      expected_version: 0,
    })
  })

  test('keeps CNY controls after USD controls and preserves its draft when USD unlimited changes', async () => {
    installApiFixtures([])
    const currentRow: ApiKey = {
      id: 42,
      name: 'dual-currency',
      key: '',
      status: 1,
      remain_quota: 5000000,
      used_quota: 0,
      unlimited_quota: false,
      expired_time: -1,
      created_time: 1,
      accessed_time: 0,
      group: 'default',
      auto_groups: [],
      cross_group_retry: false,
      model_limits_enabled: false,
      model_limits: '',
      allow_ips: '',
      allow_user_quota_query: true,
    }
    const budget = {
      enabled: true,
      remain_quota: 500000,
      used_quota: 0,
      unlimited_quota: false,
      version: 2,
      quota_per_unit: 500000,
    }
    const getFixture = apiClient.get
    vi.spyOn(apiClient, 'get').mockImplementation(async (url) => {
      if (url === '/api/token/42') {
        return { data: { success: true, data: currentRow } }
      }
      if (url === '/api/token/42/currency/CNY') {
        return { data: { success: true, data: budget } }
      }
      return getFixture(url)
    })
    const put = vi.spyOn(api, 'put').mockResolvedValue({
      data: { success: true, data: { ...budget, remain_quota: 2000000 } },
    })
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    })
    renderedDrawer = { queryClient }
    render(
      <QueryClientProvider client={queryClient}>
        <I18nextProvider i18n={i18n}>
          <ApiKeysProvider>
            <ApiKeysMutateDrawer
              open
              currentRow={currentRow}
              onOpenChange={() => undefined}
            />
          </ApiKeysProvider>
        </I18nextProvider>
      </QueryClientProvider>
    )
    const cnyAmount = await screen.findByRole('spinbutton', {
      name: 'Remaining CNY quota',
    })
    await waitFor(() => expect(findButton('Save changes', true)).toBeEnabled())
    const usdUnlimited = screen.getByRole('switch', { name: 'Unlimited Quota' })
    expect(usdUnlimited.compareDocumentPosition(cnyAmount)).toBe(
      Node.DOCUMENT_POSITION_FOLLOWING
    )
    fireEvent.change(cnyAmount, { target: { value: '4' } })
    fireEvent.click(usdUnlimited)
    expect(screen.queryByRole('spinbutton', { name: 'Quota (USD)' })).toBeNull()
    expect(cnyAmount).toBeVisible()
    expect(cnyAmount).toHaveValue(4)
    fireEvent.click(usdUnlimited)
    expect(screen.getByRole('spinbutton', { name: 'Quota (USD)' })).toHaveValue(
      10
    )
    expect(cnyAmount).toHaveValue(4)
    expect(screen.queryByRole('button', { name: 'Save CNY budget' })).toBeNull()
    fireEvent.click(findButton('Save changes', true))
    await waitFor(() =>
      expect(put).toHaveBeenCalledWith(
        '/api/token/',
        expect.objectContaining({
          id: 42,
          cny_budget: {
            enabled: true,
            remain_quota: 2000000,
            unlimited_quota: false,
            expected_version: 2,
          },
        })
      )
    )
  })
})
