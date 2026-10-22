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
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import type { ColumnDef } from '@tanstack/react-table'
import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { CopyButton } from '@/components/copy-button'
import { DataTablePage, useDataTable } from '@/components/data-table'
import { ErrorState } from '@/components/error-state'
import { Button } from '@/components/ui/button'
import { toIntlLocale } from '@/i18n/languages'
import { formatNumber } from '@/lib/format'

import { deleteAsset, listAssets } from '../api'
import type { Asset } from '../types'
import { AssetFormDialog } from './asset-form-dialog'
import { AssetProvidersDialog } from './asset-providers-dialog'
import { AssetStatus } from './asset-status'

export function AssetsTable() {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const queryClient = useQueryClient()
  const [pagination, setPagination] = useState({ pageIndex: 0, pageSize: 20 })
  const [rename, setRename] = useState<Asset | null>(null)
  const [remove, setRemove] = useState<Asset | null>(null)
  const [providers, setProviders] = useState<Asset | null>(null)
  const query = useQuery({
    queryKey: ['assets', pagination.pageIndex, pagination.pageSize],
    queryFn: () => listAssets(pagination.pageIndex + 1, pagination.pageSize),
    refetchInterval: 10000,
  })
  const deletion = useMutation({
    mutationFn: deleteAsset,
    onSuccess: () => {
      setRemove(null)
      void queryClient.invalidateQueries({ queryKey: ['assets'] })
    },
  })
  const columns = useMemo<ColumnDef<Asset, unknown>[]>(
    () => [
      {
        accessorKey: 'name',
        header: t('Name'),
        cell: ({ row }) => (
          <div className='max-w-72 min-w-0'>
            <span className='block truncate' title={row.original.name}>
              {row.original.name}
            </span>
            <div className='flex items-center gap-1'>
              <code className='text-muted-foreground truncate text-xs'>
                {row.original.id}
              </code>
              {row.original.status === 'ready' && (
                <CopyButton
                  value={`asset://${row.original.id}`}
                  aria-label={t('Copy asset reference')}
                />
              )}
            </div>
          </div>
        ),
      },
      {
        accessorKey: 'kind',
        header: t('Type'),
        cell: ({ row }) => {
          const labels = {
            image: t('Image'),
            video: t('Video'),
            audio: t('Audio'),
          }
          return labels[row.original.kind]
        },
      },
      {
        accessorKey: 'size',
        header: t('Size'),
        cell: ({ row }) =>
          `${formatNumber(row.original.size / 1024 / 1024, locale)} MiB`,
      },
      {
        accessorKey: 'status',
        header: t('Status'),
        cell: ({ row }) => (
          <div className='max-w-72 space-y-1'>
            <AssetStatus status={row.original.status} />
            {row.original.error && (
              <p className='text-destructive text-xs break-words'>
                {t(row.original.error)}
              </p>
            )}
          </div>
        ),
      },
      {
        id: 'actions',
        header: t('Actions'),
        cell: ({ row }) => (
          <div className='flex gap-1'>
            <Button
              variant='ghost'
              size='sm'
              onClick={() => setProviders(row.original)}
            >
              {t('Providers')}
            </Button>
            <Button
              variant='ghost'
              size='sm'
              disabled={row.original.status !== 'ready'}
              onClick={() => setRename(row.original)}
            >
              {t('Rename')}
            </Button>
            <Button
              variant='ghost'
              size='sm'
              disabled={
                row.original.status === 'deleted' ||
                row.original.status === 'uploading'
              }
              onClick={() => setRemove(row.original)}
            >
              {t('Delete')}
            </Button>
          </div>
        ),
      },
    ],
    [t, locale]
  )
  const { table } = useDataTable({
    data: query.data?.items ?? [],
    columns,
    totalCount: query.data?.total ?? 0,
    pagination,
    onPaginationChange: setPagination,
    manualPagination: true,
    enableSorting: false,
    getRowId: (row) => row.id,
    ensurePageInRange: (pageCount) => {
      if (
        query.isSuccess &&
        !query.isFetching &&
        pagination.pageIndex >= Math.max(1, pageCount)
      ) {
        setPagination((previous) => ({
          ...previous,
          pageIndex: Math.max(0, pageCount - 1),
        }))
      }
    },
  })
  if (query.isError) {
    return (
      <ErrorState
        onRetry={() => {
          void query.refetch()
        }}
      />
    )
  }
  return (
    <>
      <DataTablePage
        table={table}
        columns={columns}
        isLoading={query.isLoading}
        isFetching={query.isFetching}
        toolbarProps={null}
        emptyTitle={t('No assets yet')}
        emptyDescription={t('Upload an asset to reuse it in video requests.')}
      />
      {rename && (
        <AssetFormDialog asset={rename} onClose={() => setRename(null)} />
      )}
      {providers && (
        <AssetProvidersDialog
          asset={providers}
          onClose={() => setProviders(null)}
        />
      )}
      <ConfirmDialog
        open={Boolean(remove)}
        onOpenChange={(open) => {
          if (!open && !deletion.isPending) setRemove(null)
        }}
        title={t('Delete asset?')}
        desc={t(
          'New requests cannot use this asset. Existing tasks finish before its files are removed.'
        )}
        destructive
        isLoading={deletion.isPending}
        confirmText={t('Delete')}
        handleConfirm={() => {
          if (remove) deletion.mutate(remove.id)
        }}
      />
    </>
  )
}
