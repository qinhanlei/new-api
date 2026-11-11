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
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { getServerErrorMessage } from '@/lib/server-error-message'

import { assetQueryKeys, deleteAssetsBatch } from '../api'
import type { Asset, AssetBatchDeleteItem, AssetScope } from '../types'

export interface AssetBatchDeleteTarget {
  assets: Pick<Asset, 'Id' | 'Name'>[]
  project: string
  scope: AssetScope
}

export function AssetBatchDeleteDialog(props: {
  target: AssetBatchDeleteTarget
  disabled: boolean
  finalFocus: (completed: boolean) => HTMLElement | null
  onClose: () => void
  onDeleted: (ids: string[]) => void
}) {
  const { t } = useTranslation()
  const client = useQueryClient()
  const [remaining, setRemaining] = useState(props.target.assets)
  const [results, setResults] = useState<AssetBatchDeleteItem[]>([])
  const submitting = useRef(false)
  const completed = useRef(false)
  const remove = useMutation({
    mutationFn: () =>
      deleteAssetsBatch(
        props.target.project,
        remaining.map((asset) => asset.Id),
        props.target.scope
      ),
    retry: false,
    meta: { errorToast: false },
    onSuccess: (items) => {
      const combined = new Map(results.map((item) => [item.Id, item]))
      for (const item of items) combined.set(item.Id, item)
      const next = [...combined.values()]
      setResults(next)
      const failed = new Set(
        items.filter((item) => item.Status === 'failed').map((item) => item.Id)
      )
      setRemaining((previous) =>
        previous.filter((asset) => failed.has(asset.Id))
      )
      props.onDeleted(
        items.filter((item) => item.Status !== 'failed').map((item) => item.Id)
      )
      if (failed.size === 0) {
        // The trigger disappears with the selection in the same React commit.
        completed.current = true
        toast.success(
          t('Deleted: {{deleted}}. Not found: {{missing}}.', {
            deleted: next.filter((item) => item.Status === 'deleted').length,
            missing: next.filter((item) => item.Status === 'not_found').length,
          })
        )
        props.onClose()
      }
    },
    onSettled: () => {
      submitting.current = false
      void client.invalidateQueries({ queryKey: assetQueryKeys.all })
    },
  })

  return (
    <ConfirmDialog
      open
      finalFocus={() => props.finalFocus(completed.current)}
      title={t('Delete selected assets')}
      desc={t(
        'Delete {{count}} selected assets? Their asset URIs will no longer work.',
        { count: remaining.length }
      )}
      destructive
      confirmText={remove.isError || results.length ? t('Retry') : t('Delete')}
      isLoading={remove.isPending}
      disabled={props.disabled || remaining.length === 0}
      onOpenChange={(open) => !open && !submitting.current && props.onClose()}
      handleConfirm={() => {
        if (submitting.current || props.disabled || remaining.length === 0) {
          return
        }
        submitting.current = true
        remove.mutate()
      }}
    >
      {results.length > 0 && (
        <div className='space-y-2 text-sm'>
          <p role='status'>
            {t(
              'Deleted: {{deleted}}. Not found: {{missing}}. Failed: {{failed}}.',
              {
                deleted: results.filter((item) => item.Status === 'deleted')
                  .length,
                missing: results.filter((item) => item.Status === 'not_found')
                  .length,
                failed: results.filter((item) => item.Status === 'failed')
                  .length,
              }
            )}
          </p>
          <ul className='max-h-60 space-y-2 overflow-y-auto'>
            {results
              .filter((item) => item.Status === 'failed')
              .map((item) => (
                <li key={item.Id} className='text-destructive break-words'>
                  <span className='font-medium'>
                    {props.target.assets.find((asset) => asset.Id === item.Id)
                      ?.Name || item.Id}
                  </span>
                  {': '}
                  {item.Error?.Code && `${item.Error.Code}: `}
                  {item.Error
                    ? t(item.Error.Message)
                    : t('The asset operation could not be completed.')}
                </li>
              ))}
          </ul>
        </div>
      )}
      {remove.isError && (
        <div role='alert' className='text-destructive space-y-1 text-sm'>
          <p>{getServerErrorMessage(remove.error)}</p>
          <p>
            {t(
              'Deletion was not confirmed. Check the refreshed list before retrying.'
            )}
          </p>
        </div>
      )}
    </ConfirmDialog>
  )
}
