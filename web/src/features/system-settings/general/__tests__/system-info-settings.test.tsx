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
  createRouter,
  RouterProvider,
} from '@tanstack/react-router'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { expect, test, vi } from 'vitest'

import { api } from '@/lib/api'

import { SettingsPageProvider } from '../../components/settings-page-context'
import { SystemInfoSection } from '../system-info-section'

function Fixture(props: { address: string }) {
  const [container, setContainer] = useState<HTMLDivElement | null>(null)
  return (
    <SettingsPageProvider actionsContainer={container}>
      <div ref={setContainer} />
      <SystemInfoSection
        defaultValues={{
          SystemName: 'New API',
          ServerAddress: props.address,
          TaskPublicAddress: '',
          Logo: '',
          Footer: '',
          About: '',
          HomePageContent: '',
          general_setting: { docs_link: '' },
          legal: { user_agreement: '', privacy_policy: '' },
        }}
      />
    </SettingsPageProvider>
  )
}

async function renderSettings(address: string) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  const router = createRouter({
    routeTree: createRootRoute({
      component: () => <Fixture address={address} />,
    }),
    history: createMemoryHistory({ initialEntries: ['/'] }),
  })
  render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  )
  return screen.findByRole('textbox', { name: 'Server Address' })
}

const localAddressWarning =
  'This address appears to be local. Human verification providers may reject it; check your public access address.'

test.each(['https://gateway.example', 'https://10.auth.example.com'])(
  'does not infer that domain %s is private from its name',
  async (address) => {
    await renderSettings(address)
    expect(screen.queryByText(localAddressWarning)).not.toBeInTheDocument()
  }
)

test.each([
  'http://192.168.2.101:3000',
  'https://localhost',
  'http://10.0.0.1',
  'http://172.16.1.2',
  'http://[::1]:3000',
  'https://[fd00::1]',
])(
  'warns about local callback address %s without marking it invalid',
  async (address) => {
    const input = await renderSettings(address)
    expect(screen.getByText(localAddressWarning)).toBeVisible()
    expect(input).not.toHaveAttribute('aria-invalid', 'true')
  }
)

test('updating a local address to a public HTTP origin clears the warning and saves HTTP unchanged', async () => {
  const put = vi
    .spyOn(api, 'put')
    .mockResolvedValue({ data: { success: true } })
  const user = userEvent.setup()
  const input = await renderSettings('http://192.168.2.101:3000')
  expect(screen.getByText(localAddressWarning)).toBeVisible()
  fireEvent.change(input, { target: { value: 'http://203.0.113.20:13000' } })
  expect(screen.queryByText(localAddressWarning)).not.toBeInTheDocument()
  await user.click(screen.getByRole('button', { name: 'Save Changes' }))
  await waitFor(() =>
    expect(put).toHaveBeenCalledWith('/api/option/', {
      key: 'ServerAddress',
      value: 'http://203.0.113.20:13000',
    })
  )
})

test('local address guidance remains advisory and does not prevent saving', async () => {
  const put = vi
    .spyOn(api, 'put')
    .mockResolvedValue({ data: { success: true } })
  const user = userEvent.setup()
  const input = await renderSettings('https://gateway.example')
  expect(screen.queryByText(localAddressWarning)).not.toBeInTheDocument()
  fireEvent.change(input, { target: { value: 'http://127.0.0.1:3000' } })
  expect(screen.getByText(localAddressWarning)).toBeVisible()
  await user.click(screen.getByRole('button', { name: 'Save Changes' }))
  await waitFor(() =>
    expect(put).toHaveBeenCalledWith('/api/option/', {
      key: 'ServerAddress',
      value: 'http://127.0.0.1:3000',
    })
  )
})
