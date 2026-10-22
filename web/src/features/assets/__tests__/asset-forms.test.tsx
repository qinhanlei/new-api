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
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ReactNode } from 'react'
import { describe, expect, test, vi } from 'vitest'

import { api } from '@/lib/api'

import { AssetFormDialog } from '../components/asset-form-dialog'
import type { Asset } from '../types'

function renderForm(component: ReactNode) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  return render(
    <QueryClientProvider client={client}>{component}</QueryClientProvider>
  )
}

const asset: Asset = {
  id: 'na_asset_example',
  name: 'Reference',
  kind: 'image',
  mime_type: 'image/png',
  size: 25,
  status: 'ready',
  created_at: 1,
}
describe('asset forms', () => {
  test('file upload sends immutable bytes and closes only after a successful response', async () => {
    const user = userEvent.setup()
    const onClose = vi.fn()
    const post = vi
      .spyOn(api, 'post')
      .mockResolvedValue({ data: { success: true, data: asset } })
    renderForm(<AssetFormDialog onClose={onClose} />)
    const file = new File(['image'], 'reference.png', { type: 'image/png' })
    await user.upload(screen.getByLabelText('File'), file)
    expect(screen.getByLabelText('Name')).toHaveValue('reference.png')
    await user.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(onClose).toHaveBeenCalledOnce())
    expect(post).toHaveBeenCalledWith('/api/assets', expect.any(FormData))
    const form = post.mock.calls[0][1] as FormData
    expect(form.get('file')).toBe(file)
    expect(form.get('name')).toBe('reference.png')
  })

  test('switching back to file upload requires selecting a file again', async () => {
    const user = userEvent.setup()
    const post = vi
      .spyOn(api, 'post')
      .mockResolvedValue({ data: { success: true, data: asset } })
    renderForm(<AssetFormDialog onClose={vi.fn()} />)
    await user.upload(
      screen.getByLabelText('File'),
      new File(['image'], 'reference.png', { type: 'image/png' })
    )
    await user.click(screen.getByRole('tab', { name: 'Import URL' }))
    await user.click(screen.getByRole('tab', { name: 'File' }))
    await user.click(screen.getByRole('button', { name: 'Save' }))
    expect(await screen.findByText('Select a file')).toBeVisible()
    expect(post).not.toHaveBeenCalled()
  })

  test('invalid import URL stays open and reports the invalid field', async () => {
    const user = userEvent.setup()
    const post = vi.spyOn(api, 'post')
    renderForm(<AssetFormDialog onClose={vi.fn()} />)
    await user.click(screen.getByRole('tab', { name: 'Import URL' }))
    await user.type(screen.getByLabelText('Source URL'), 'file:///etc/passwd')
    await user.type(screen.getByLabelText('Name'), 'source')
    await user.click(screen.getByRole('button', { name: 'Save' }))
    expect(
      await screen.findByText('Enter a valid HTTP or HTTPS URL')
    ).toBeVisible()
    expect(screen.getByLabelText('Source URL')).toHaveAttribute(
      'aria-invalid',
      'true'
    )
    expect(post).not.toHaveBeenCalled()
  })

  test('failed rename keeps the entered name and dialog available for retry', async () => {
    const user = userEvent.setup()
    const onClose = vi.fn()
    const patch = vi.spyOn(api, 'patch').mockResolvedValue({
      data: { success: false, message: 'Asset not found' },
    })
    renderForm(<AssetFormDialog asset={asset} onClose={onClose} />)
    await user.clear(screen.getByLabelText('Name'))
    await user.type(screen.getByLabelText('Name'), 'New name')
    await user.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(patch).toHaveBeenCalled())
    await waitFor(() =>
      expect(screen.getByRole('button', { name: 'Save' })).toBeEnabled()
    )
    expect(onClose).not.toHaveBeenCalled()
    expect(screen.getByLabelText('Name')).toHaveValue('New name')
  })
})
