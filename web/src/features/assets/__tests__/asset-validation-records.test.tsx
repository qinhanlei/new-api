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
  fireEvent,
  isInaccessible,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { toast } from 'sonner'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'

import { api } from '@/lib/api'
import { ROLE } from '@/lib/roles'
import { useAuthStore } from '@/stores/auth-store'

import { AssetValidationRecordsDrawer } from '../components/asset-validation-records'
import type { AssetValidationSummary } from '../types'

const completed: AssetValidationSummary = {
  SessionId: 'record-completed',
  Name: 'Verified person',
  Status: 'created',
  GroupId: 'group-exact',
  ChannelId: 7,
  ChannelName: 'Portrait channel',
  ProjectName: 'portraits',
  ExpiresAt: 1,
  CanPoll: false,
  CanContinue: false,
  NeedsReview: false,
  BindingPending: false,
  CanArchive: true,
  ArchivedAt: 0,
  CreatedAt: 1_700_000_000,
  LastCheckedAt: 1_700_000_020,
}
const unresolved: AssetValidationSummary = {
  ...completed,
  SessionId: 'record-unresolved',
  Name: 'Needs investigation',
  Status: 'expired',
  GroupId: undefined,
  NeedsReview: true,
  LastCheckedAt: 0,
  Error: {
    Code: 'ValidationPending',
    Message: 'No confirmed result is available',
  },
}
let client: QueryClient
let records: AssetValidationSummary[]
let totalCount: number

function renderRecords(
  overrides: Partial<
    React.ComponentProps<typeof AssetValidationRecordsDrawer>
  > = {}
) {
  const props = {
    project: 'portraits',
    scope: { userId: 23 },
    onClose: vi.fn(),
    onContinue: vi.fn(),
    onViewGroup: vi.fn(),
    onStartNew: vi.fn(),
    ...overrides,
  }
  render(
    <QueryClientProvider client={client}>
      <AssetValidationRecordsDrawer {...props} />
    </QueryClientProvider>
  )
  return props
}

beforeEach(() => {
  client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  records = [{ ...completed }, { ...unresolved }]
  totalCount = records.length
  vi.spyOn(api, 'get').mockImplementation(async (url, config) => {
    const data =
      url === '/api/assets/validation/records'
        ? {
            Items: records,
            TotalCount: totalCount,
            PendingCount: 1,
            Page: (config?.params as { page?: number } | undefined)?.page ?? 1,
            PageSize: 20,
          }
        : records.find(
            (record) =>
              url === `/api/assets/validation/records/${record.SessionId}`
          )
    return { data: { success: true, data } }
  })
})

afterEach(() => {
  client.clear()
  vi.useRealTimers()
  useAuthStore.getState().auth.reset()
})

test('completed records open the exact group without querying the verification result', async () => {
  const user = userEvent.setup()
  const post = vi.spyOn(api, 'post')
  const success = vi.spyOn(toast, 'success')
  const props = renderRecords()
  await user.click(await screen.findByRole('button', { name: 'View group' }))
  expect(props.onViewGroup).toHaveBeenCalledWith('group-exact')
  expect(post).not.toHaveBeenCalled()
  expect(success).not.toHaveBeenCalled()
})

test('expired uncertain records expose read-only details and no invalid continue action', async () => {
  const user = userEvent.setup()
  useAuthStore.getState().auth.setUser({
    id: 1,
    username: 'admin',
    role: ROLE.ADMIN,
    permissions: { admin_permissions: { channel: { read: true } } },
  })
  const post = vi.spyOn(api, 'post')
  renderRecords()
  await user.click(
    await screen.findByRole('button', {
      name: 'View details: Needs investigation',
    })
  )
  expect(
    await screen.findByRole('heading', { name: 'Needs investigation' })
  ).toBeVisible()
  expect(screen.getByText('Not recorded')).toBeVisible()
  expect(
    screen.getByText(
      'The link has expired. The verification result still needs to be checked with the provider.'
    )
  ).toBeVisible()
  expect(
    screen.queryByRole('button', { name: 'Continue verification' })
  ).not.toBeInTheDocument()
  await user.click(screen.getByRole('button', { name: 'Diagnostics' }))
  expect(screen.getByText('ValidationPending')).toBeVisible()
  expect(api.get).toHaveBeenCalledWith(
    '/api/assets/validation/records/record-unresolved',
    expect.objectContaining({
      params: { project_name: 'portraits', user_id: 23 },
    })
  )
  expect(post).not.toHaveBeenCalled()
})

test('another sign-in keeps the verification status and shows its separate continuation restriction', async () => {
  const user = userEvent.setup()
  records = [
    {
      ...completed,
      SessionId: 'record-pending',
      Name: 'Waiting person',
      GroupId: undefined,
      Status: 'pending',
      CanPoll: true,
      CanContinue: false,
      CanArchive: false,
      ExpiresAt: 9_000_000_000,
      ContinueError: {
        Code: 'DifferentSession',
        Message: 'Return to the sign-in session that started this verification',
      },
    },
  ]
  renderRecords()
  await user.click(
    await screen.findByRole('button', { name: 'View details: Waiting person' })
  )
  expect(
    await screen.findByText(
      'Return to the sign-in session that started this verification'
    )
  ).toBeVisible()
  expect(
    screen.queryByText(
      'Open this verification to continue checking its result.'
    )
  ).not.toBeInTheDocument()
  expect(
    screen.queryByRole('button', { name: 'Continue verification' })
  ).not.toBeInTheDocument()
  expect(
    screen.queryByRole('button', { name: 'Archive' })
  ).not.toBeInTheDocument()
  expect(
    screen.queryByRole('button', { name: 'Start a new verification' })
  ).not.toBeInTheDocument()
})

test('a saved upstream result can continue binding after the verification link expires', async () => {
  const user = userEvent.setup()
  const binding = {
    ...completed,
    SessionId: 'record-binding',
    Name: 'Binding person',
    Status: 'verifying' as const,
    GroupId: undefined,
    CanContinue: true,
    CanPoll: true,
    CanArchive: false,
    BindingPending: true,
  }
  records = [binding]
  const props = renderRecords()
  await user.click(
    await screen.findByRole('button', { name: 'Continue binding' })
  )
  expect(props.onContinue).toHaveBeenCalledWith(binding)
  expect(props.onViewGroup).not.toHaveBeenCalled()
})

test('filters and page survive opening details and returning to the same list', async () => {
  const user = userEvent.setup()
  totalCount = 41
  renderRecords()
  await screen.findByText('Verified person')
  await user.selectOptions(
    screen.getByRole('combobox', { name: 'Verification status' }),
    'completed'
  )
  await waitFor(() =>
    expect(api.get).toHaveBeenLastCalledWith(
      '/api/assets/validation/records',
      expect.objectContaining({
        params: expect.objectContaining({ status: 'completed', page: 1 }),
      })
    )
  )
  await user.click(screen.getByRole('button', { name: 'Go to next page' }))
  await waitFor(() =>
    expect(api.get).toHaveBeenLastCalledWith(
      '/api/assets/validation/records',
      expect.objectContaining({
        params: expect.objectContaining({ status: 'completed', page: 2 }),
      })
    )
  )
  await user.click(
    screen.getByRole('button', { name: 'View details: Verified person' })
  )
  await screen.findByRole('heading', { name: 'Verified person' })
  expect(screen.getByRole('button', { name: 'Back to records' })).toHaveFocus()
  await user.click(screen.getByRole('button', { name: 'Back to records' }))
  expect(
    screen.getByRole('button', { name: 'View details: Verified person' })
  ).toHaveFocus()
  expect(
    screen.getByRole('combobox', { name: 'Verification status' })
  ).toHaveValue('completed')
  expect(screen.getByRole('navigation', { name: 'Page' })).toHaveTextContent(
    '2 / 3'
  )
})

test('uncertain records require individual archive confirmation and remain included in the pending count', async () => {
  const user = userEvent.setup()
  const post = vi.spyOn(api, 'post').mockImplementation(async (_url, body) => {
    const request = body as { SessionIds: string[]; Archived: boolean }
    records = records.filter(
      (record) => !request.SessionIds.includes(record.SessionId)
    )
    totalCount = records.length
    return { data: { success: true, data: null } }
  })
  renderRecords()
  expect(
    await screen.findByRole('checkbox', { name: 'Select Needs investigation' })
  ).toHaveAttribute('aria-disabled', 'true')
  await user.click(
    screen.getByRole('button', { name: 'View details: Needs investigation' })
  )
  await user.click(await screen.findByRole('button', { name: 'Archive' }))
  const confirm = await screen.findByRole('alertdialog')
  expect(
    within(confirm).getByText(
      'Archiving only hides this record from the active list. It still needs review and remains included in the pending count.'
    )
  ).toBeVisible()
  await user.click(within(confirm).getByRole('button', { name: 'Archive' }))
  await waitFor(() =>
    expect(post).toHaveBeenCalledWith(
      '/api/assets/validation/records/archive',
      {
        ProjectName: 'portraits',
        SessionIds: ['record-unresolved'],
        Archived: true,
      },
      expect.objectContaining({ params: { user_id: 23 } })
    )
  )
  expect(
    await screen.findByText('1 pending, including archived records')
  ).toBeVisible()
})

test('batch archive submits only selected safe records and failed requests preserve selection', async () => {
  const user = userEvent.setup()
  records = [
    completed,
    { ...completed, SessionId: 'record-two', Name: 'Second person' },
    unresolved,
  ]
  totalCount = records.length
  const post = vi
    .spyOn(api, 'post')
    .mockRejectedValue(new Error('Archive temporarily unavailable'))
  renderRecords()
  await user.click(
    await screen.findByRole('checkbox', { name: 'Select Verified person' })
  )
  await user.click(
    screen.getByRole('checkbox', { name: 'Select Second person' })
  )
  await user.click(screen.getByRole('button', { name: 'Archive selected' }))
  const confirm = await screen.findByRole('alertdialog')
  await user.click(within(confirm).getByRole('button', { name: 'Archive' }))
  expect(await within(confirm).findByRole('alert')).toHaveTextContent(
    'Archive temporarily unavailable'
  )
  expect(post).toHaveBeenCalledWith(
    '/api/assets/validation/records/archive',
    {
      ProjectName: 'portraits',
      SessionIds: ['record-completed', 'record-two'],
      Archived: true,
    },
    expect.anything()
  )
  await user.click(within(confirm).getByRole('button', { name: 'Cancel' }))
  expect(
    screen.getByRole('checkbox', { name: 'Select Verified person' })
  ).toBeChecked()
  expect(
    screen.getByRole('checkbox', { name: 'Select Second person' })
  ).toBeChecked()
})

test('restoring archived records never invokes verification and returns them to the active list', async () => {
  const user = userEvent.setup()
  records = [{ ...unresolved, ArchivedAt: 1_700_000_100 }]
  totalCount = 1
  const post = vi
    .spyOn(api, 'post')
    .mockResolvedValue({ data: { success: true, data: null } })
  renderRecords()
  await user.selectOptions(
    await screen.findByRole('combobox', { name: 'Archive status' }),
    'archived'
  )
  await user.click(
    await screen.findByRole('checkbox', { name: 'Select Needs investigation' })
  )
  await user.click(screen.getByRole('button', { name: 'Restore selected' }))
  await waitFor(() =>
    expect(post).toHaveBeenCalledWith(
      '/api/assets/validation/records/archive',
      {
        ProjectName: 'portraits',
        SessionIds: ['record-unresolved'],
        Archived: false,
      },
      expect.anything()
    )
  )
  expect(post.mock.calls).toHaveLength(1)
})

test('a missing historical group shows the failure without selecting another group or closing', async () => {
  const user = userEvent.setup()
  const props = renderRecords({
    onViewGroup: vi
      .fn()
      .mockRejectedValue(new Error('The associated group is unavailable')),
  })
  await user.click(await screen.findByRole('button', { name: 'View group' }))
  expect(await screen.findByRole('alert')).toHaveTextContent(
    'The associated group is unavailable'
  )
  expect(props.onClose).not.toHaveBeenCalled()
  expect(props.onContinue).not.toHaveBeenCalled()
})

test('refresh removes selection when the record becomes ineligible but retains other selected records', async () => {
  const user = userEvent.setup()
  records = [
    completed,
    { ...completed, SessionId: 'record-two', Name: 'Second person' },
  ]
  renderRecords()
  await user.click(
    await screen.findByRole('checkbox', { name: 'Select Verified person' })
  )
  await user.click(
    screen.getByRole('checkbox', { name: 'Select Second person' })
  )
  records = [{ ...completed, CanArchive: false }, records[1]]
  await user.click(screen.getByRole('button', { name: 'Refresh records' }))
  await waitFor(() =>
    expect(
      screen.getByRole('checkbox', { name: 'Select Verified person' })
    ).toHaveAttribute('aria-disabled', 'true')
  )
  expect(
    screen.getByRole('checkbox', { name: 'Select Verified person' })
  ).not.toBeChecked()
  expect(
    screen.getByRole('checkbox', { name: 'Select Second person' })
  ).toBeChecked()
})

test('record load errors offer retry and an empty result keeps filters and new verification available', async () => {
  const user = userEvent.setup()
  vi.mocked(api.get).mockRejectedValueOnce(new Error('Records unavailable'))
  const props = renderRecords()
  expect(await screen.findByText('Records unavailable')).toBeVisible()
  records = []
  totalCount = 0
  await user.click(screen.getByRole('button', { name: 'Retry' }))
  expect(await screen.findByText('No verification records')).toBeVisible()
  expect(
    screen.getByRole('combobox', { name: 'Verification status' })
  ).toBeEnabled()
  await user.click(screen.getByRole('button', { name: 'Start verification' }))
  expect(props.onStartNew).toHaveBeenCalledOnce()
})

test('a narrow drawer keeps long record names and the detail back action accessible', async () => {
  const user = userEvent.setup()
  const matchMedia = window.matchMedia
  vi.spyOn(window, 'matchMedia').mockImplementation((query) => ({
    ...matchMedia(query),
    matches: query.includes('max-width: 640px') || matchMedia(query).matches,
  }))
  const name = 'A long verification record name '.repeat(5)
  records = [{ ...completed, Name: name }]
  renderRecords()
  await user.click(
    await screen.findByRole('button', { name: `View details: ${name}`.trim() })
  )
  const heading = await screen.findByRole('heading', { name: name.trim() })
  expect(heading).toHaveClass('break-words')
  expect(screen.getByRole('dialog')).toHaveClass('w-full', 'h-dvh')
  await user.click(screen.getByRole('button', { name: 'Back to records' }))
  expect(
    screen.getByRole('button', { name: `View details: ${name}`.trim() })
  ).toBeVisible()
  fireEvent.keyDown(screen.getByRole('dialog'), {
    key: 'Escape',
    code: 'Escape',
  })
})

test('mobile selection disables records that cannot be managed in bulk', async () => {
  const user = userEvent.setup()
  const matchMedia = window.matchMedia
  vi.spyOn(window, 'matchMedia').mockImplementation((query) => ({
    ...matchMedia(query),
    matches: query.includes('max-width: 640px') || matchMedia(query).matches,
  }))
  renderRecords()
  const unavailable = await screen.findByRole('checkbox', {
    name: 'Select row 2',
  })
  expect(unavailable).toHaveAttribute('aria-disabled', 'true')
  await user.click(unavailable)
  expect(unavailable).not.toBeChecked()
  await user.click(screen.getByRole('checkbox', { name: 'Select row 1' }))
  expect(screen.getByRole('button', { name: 'Archive selected' })).toBeEnabled()
  records = [{ ...unresolved, CanArchive: false }]
  await user.click(screen.getByRole('button', { name: 'Refresh records' }))
  await waitFor(() =>
    expect(
      screen.getByRole('checkbox', { name: 'Select all' })
    ).toHaveAttribute('aria-disabled', 'true')
  )
})

test.each([ROLE.USER, ROLE.ADMIN])(
  'verification details hide channel information and diagnostics without channel read permission for role %s',
  async (role) => {
    const user = userEvent.setup()
    useAuthStore.getState().auth.setUser({
      id: 1,
      username: 'caller',
      role,
      permissions: { admin_permissions: { channel: { read: false } } },
    })
    renderRecords()
    await user.click(
      await screen.findByRole('button', {
        name: 'View details: Needs investigation',
      })
    )
    expect(
      await screen.findByRole('heading', { name: 'Needs investigation' })
    ).toBeVisible()
    expect(screen.queryByText('Portrait channel')).not.toBeInTheDocument()
    expect(screen.queryByText('Channel')).not.toBeInTheDocument()
    expect(
      screen.queryByRole('button', { name: 'Diagnostics' })
    ).not.toBeInTheDocument()
    expect(screen.getByText('No confirmed result is available')).toBeVisible()
  }
)

test.each([
  {
    status: 'failed' as const,
    nextPollDelay: -60,
    bindingPending: false,
    label: 'Verification could not start',
  },
  {
    status: 'expired' as const,
    nextPollDelay: 60,
    bindingPending: false,
    label: 'Verification link expired',
  },
  {
    status: 'unknown' as const,
    nextPollDelay: 60,
    bindingPending: true,
    label: 'Linking person group',
  },
])(
  'a $status record uses $label instead of an obsolete or lower-priority throttle state',
  async ({ status, nextPollDelay, bindingPending, label }) => {
    records = [
      {
        ...completed,
        SessionId: 'record-limited',
        Name: 'Limited attempt',
        GroupId: undefined,
        Status: status,
        ExpiresAt:
          status === 'expired' || bindingPending
            ? 1
            : Math.floor(Date.now() / 1000) + 1800,
        BindingPending: bindingPending,
        NextPollAt: Math.floor(Date.now() / 1000) + nextPollDelay,
        Error: {
          Code: 'Throttling',
          Message: 'Human verification is busy; retry shortly',
        },
      },
    ]
    renderRecords()
    expect(
      await within(screen.getByRole('table')).findByText(label)
    ).toBeVisible()
    expect(screen.queryByText('Waiting to retry')).not.toBeInTheDocument()
  }
)

test('an open verification record updates after its cooldown without refreshing the record', async () => {
  vi.useFakeTimers()
  vi.setSystemTime(new Date('2026-11-02T00:00:00Z'))
  records = [
    {
      ...completed,
      SessionId: 'record-limited',
      Name: 'Limited attempt',
      Status: 'failed',
      GroupId: undefined,
      ExpiresAt: Date.now() / 1000 + 1800,
      NextPollAt: Date.now() / 1000 + 60,
      Error: {
        Code: 'Throttling',
        Message: 'Human verification is busy; retry shortly',
      },
    },
  ]
  renderRecords()
  await act(async () => {
    await vi.advanceTimersByTimeAsync(0)
  })
  expect(
    within(screen.getByRole('table')).getByText('Waiting to retry')
  ).toBeVisible()
  fireEvent.click(
    screen.getByRole('button', { name: 'View details: Limited attempt' })
  )
  await act(async () => {
    await vi.advanceTimersByTimeAsync(0)
  })
  expect(screen.getByRole('heading', { name: 'Limited attempt' })).toBeVisible()
  const requestCount = vi.mocked(api.get).mock.calls.length
  await act(async () => {
    await vi.advanceTimersByTimeAsync(60_000)
  })
  expect(
    screen
      .getAllByText('Verification could not start')
      .filter((label) => !isInaccessible(label))
  ).toHaveLength(1)
  for (const label of screen.queryAllByText('Waiting to retry')) {
    expect(label).not.toBeVisible()
  }
  expect(api.get).toHaveBeenCalledTimes(requestCount)
})

test('a verification list updates its throttle label when unchanged records remain cached', async () => {
  vi.useFakeTimers()
  vi.setSystemTime(new Date('2026-11-02T00:00:00Z'))
  records = [
    {
      ...completed,
      SessionId: 'record-limited',
      Name: 'Limited attempt',
      Status: 'failed',
      GroupId: undefined,
      ExpiresAt: Date.now() / 1000 + 1800,
      NextPollAt: Date.now() / 1000 + 60,
      Error: {
        Code: 'Throttling',
        Message: 'Human verification is busy; retry shortly',
      },
    },
  ]
  renderRecords()
  await act(async () => {
    await vi.advanceTimersByTimeAsync(0)
  })
  const table = screen.getByRole('table')
  expect(within(table).getByText('Waiting to retry')).toBeVisible()
  await act(async () => {
    await vi.advanceTimersByTimeAsync(60_000)
  })
  expect(within(table).getByText('Verification could not start')).toBeVisible()
  expect(within(table).queryByText('Waiting to retry')).not.toBeInTheDocument()
})
