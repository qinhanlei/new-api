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
import type {
  CellContext,
  ColumnDef,
  HeaderContext,
  RowSelectionState,
} from '@tanstack/react-table'
import { Trash2 } from 'lucide-react'
import { useMemo, useRef, useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { CopyButton } from '@/components/copy-button'
import {
  DataTablePage,
  DataTableBulkActions,
  DataTableToolbar,
  TruncatedCell,
  useDataTable,
  type DataTablePageProps,
} from '@/components/data-table'
import { Dialog } from '@/components/dialog'
import { ErrorState } from '@/components/error-state'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { toIntlLocale } from '@/i18n/languages'
import {
  ADMIN_PERMISSION_ACTIONS,
  ADMIN_PERMISSION_RESOURCES,
  hasPermission,
} from '@/lib/admin-permissions'
import { formatNumber } from '@/lib/format'
import { getServerErrorMessage } from '@/lib/server-error-message'
import { formatDateTimeObject } from '@/lib/time'
import { useAuthStore } from '@/stores/auth-store'

import { assetQueryKeys, executeAssetAction } from '../api'
import type { Asset, AssetGroup, AssetList, AssetScope } from '../types'
import {
  AssetBatchDeleteDialog,
  type AssetBatchDeleteTarget,
} from './asset-batch-delete-dialog'
import { AssetReplicasDialog } from './asset-replicas-dialog'
import { AssetStatus } from './asset-status'
import { AssetThumbnail } from './asset-thumbnail'

export function AssetListTable({
  group,
  scope,
  admin,
  toolbarProps,
  emptyState,
}: {
  group?: AssetGroup
  scope: AssetScope
  admin: boolean
  toolbarProps?: DataTablePageProps<Asset>['toolbarProps']
  emptyState?: ReactNode
}) {
  const { t, i18n } = useTranslation()
  const client = useQueryClient()
  const currentUser = useAuthStore((state) => state.auth.user)
  const canReadChannel =
    admin &&
    hasPermission(
      currentUser,
      ADMIN_PERMISSION_RESOURCES.CHANNEL,
      ADMIN_PERMISSION_ACTIONS.READ
    )
  const [pagination, setPagination] = useState({ pageIndex: 0, pageSize: 20 })
  const [name, setName] = useState('')
  const [deleting, setDeleting] = useState<{
    asset: Asset
    scope: AssetScope
  } | null>(null)
  const [batchDeleting, setBatchDeleting] =
    useState<AssetBatchDeleteTarget | null>(null)
  const batchDeleteTrigger = useRef<HTMLButtonElement>(null)
  const listRegion = useRef<HTMLDivElement>(null)
  const [replicas, setReplicas] = useState<Asset | null>(null)
  const [preview, setPreview] = useState<Asset | null>(null)
  const frozen = Boolean(group?.Status && group.Status !== 'active')
  const canDelete = Boolean(group) && !scope.allUsers && !frozen
  const ownerId = scope.userId
  const allUsers = scope.allUsers
  const query = useQuery({
    queryKey: [
      ...assetQueryKeys.all,
      'list',
      group?.GroupType,
      group?.Id,
      scope,
      name,
      pagination,
    ],
    enabled: Boolean(group),
    queryFn: ({ signal }) => {
      if (!group) return { Items: [], TotalCount: 0 }
      return executeAssetAction<AssetList<Asset>>(
        'ListAssets',
        {
          ProjectName: group.ProjectName,
          Filter: {
            GroupType: group.GroupType,
            GroupIds: [group.Id],
            Name: name,
          },
          PageNumber: pagination.pageIndex + 1,
          PageSize: pagination.pageSize,
        },
        scope,
        signal
      )
    },
    refetchInterval: 10_000,
    meta: { errorToast: false },
  })
  const selectionContext = JSON.stringify([
    group?.Id,
    group?.ProjectName,
    ownerId,
    allUsers,
    name,
    pagination,
    canDelete,
  ])
  const [selection, setSelection] = useState<{
    context: string
    items: Asset[] | undefined
    rows: RowSelectionState
  }>({ context: selectionContext, items: query.data?.Items, rows: {} })
  if (
    selection.context !== selectionContext ||
    selection.items !== query.data?.Items
  ) {
    const rows: RowSelectionState = {}
    if (selection.context === selectionContext) {
      for (const asset of query.data?.Items ?? []) {
        if (selection.rows[asset.Id]) rows[asset.Id] = true
      }
    }
    setSelection({ context: selectionContext, items: query.data?.Items, rows })
  }
  const remove = useMutation({
    mutationFn: (target: { asset: Asset; scope: AssetScope }) =>
      executeAssetAction(
        'DeleteAsset',
        { ProjectName: target.asset.ProjectName, Id: target.asset.Id },
        target.scope
      ),
    onSuccess: () => {
      setDeleting(null)
      if (query.data?.Items.length === 1 && pagination.pageIndex > 0) {
        setPagination({ ...pagination, pageIndex: pagination.pageIndex - 1 })
      }
      void client.invalidateQueries({ queryKey: assetQueryKeys.all })
      toast.success(t('Deleted successfully'))
    },
    meta: { errorToast: false },
  })
  const resetRemove = remove.reset
  const columns = useMemo<ColumnDef<Asset>[]>(
    () => [
      ...(canDelete
        ? [
            {
              id: 'select',
              header: ({ table }: HeaderContext<Asset, unknown>) => (
                <Checkbox
                  checked={table.getIsAllPageRowsSelected()}
                  indeterminate={table.getIsSomePageRowsSelected()}
                  onCheckedChange={(checked) =>
                    table.toggleAllPageRowsSelected(Boolean(checked))
                  }
                  aria-label={t('Select all')}
                />
              ),
              cell: ({ row }: CellContext<Asset, unknown>) => (
                <Checkbox
                  checked={row.getIsSelected()}
                  onCheckedChange={(checked) =>
                    row.toggleSelected(Boolean(checked))
                  }
                  aria-label={t('Select row {{number}}', {
                    number: row.index + 1,
                  })}
                />
              ),
              size: 40,
              minSize: 40,
              maxSize: 40,
              enableHiding: false,
            },
          ]
        : []),
      {
        id: 'preview',
        header: t('Preview'),
        size: 80,
        minSize: 80,
        maxSize: 80,
        cell: ({ row: { original: asset } }) => (
          <AssetThumbnail key={asset.Id} asset={asset} onPreview={setPreview} />
        ),
      },
      {
        id: 'name',
        accessorKey: 'Name',
        header: t('Name'),
        size: 180,
        cell: ({ row: { original: asset } }) => (
          <TruncatedCell tabIndex={0}>{asset.Name}</TruncatedCell>
        ),
      },
      {
        id: 'uri',
        header: t('Asset URI'),
        size: 300,
        cell: ({ row: { original: asset } }) => (
          <div className='flex min-w-0 items-center gap-1'>
            <TruncatedCell className='min-w-0 font-mono text-xs' tabIndex={0}>
              {`asset://${asset.Id}`}
            </TruncatedCell>
            {!frozen && (
              <CopyButton
                value={`asset://${asset.Id}`}
                tooltip={t('Copy asset URI')}
              />
            )}
          </div>
        ),
      },
      {
        id: 'type',
        accessorKey: 'AssetType',
        header: t('Asset type'),
        size: 100,
        cell: ({ row: { original: asset } }) => {
          if (asset.AssetType) return t(asset.AssetType)
          return asset.Status === 'Processing'
            ? t('Identifying...')
            : t('Unrecognized')
        },
      },
      ...(scope.allUsers
        ? [
            {
              id: 'owner',
              header: t('User ID'),
              cell: ({
                row: { original: asset },
              }: CellContext<Asset, unknown>) =>
                asset.UserId ?? group?.UserId ?? '—',
            },
          ]
        : []),
      {
        id: 'status',
        header: t('Status'),
        cell: ({ row: { original: asset } }) => (
          <div className='space-y-1'>
            <AssetStatus status={asset.Status} />
            {asset.Error?.Message && (
              <p className='text-destructive max-w-sm text-xs break-words whitespace-normal'>
                {asset.Error.Code}: {t(asset.Error.Message)}
              </p>
            )}
          </div>
        ),
      },
      {
        id: 'createdAt',
        accessorKey: 'CreateTime',
        header: t('Created At'),
        size: 180,
        cell: ({ row: { original: asset } }) => {
          const created = asset.CreateTime ? new Date(asset.CreateTime) : null
          if (!created || !Number.isFinite(created.getTime())) return '—'
          return (
            <time dateTime={asset.CreateTime}>
              {formatDateTimeObject(created)}
            </time>
          )
        },
      },
      {
        id: 'actions',
        header: t('Actions'),
        cell: ({ row: { original: asset } }) => (
          <div className='flex items-center gap-1'>
            {canReadChannel && (
              <Button
                size='sm'
                variant='ghost'
                onClick={() => setReplicas(asset)}
              >
                {t('Channel replicas')}
              </Button>
            )}
            <Button
              variant='ghost'
              size='icon-sm'
              aria-label={t('Delete {{name}}', { name: asset.Name })}
              disabled={scope.allUsers || frozen}
              onClick={() => {
                resetRemove()
                setDeleting({ asset, scope: { userId: ownerId, allUsers } })
              }}
            >
              <Trash2 className='size-4' />
            </Button>
          </div>
        ),
      },
    ],
    [
      t,
      frozen,
      scope.allUsers,
      group?.UserId,
      canReadChannel,
      resetRemove,
      canDelete,
      ownerId,
      allUsers,
    ]
  )
  const totalCount = query.data?.TotalCount ?? 0
  const maxPages = Math.floor(20_000 / pagination.pageSize)
  const pageCount = Math.min(
    Math.ceil(totalCount / pagination.pageSize),
    maxPages
  )
  const { table } = useDataTable({
    data: query.data?.Items ?? [],
    columns,
    columnFilters: [],
    totalCount,
    pageCount,
    globalFilter: name,
    onGlobalFilterChange: (updater) => {
      setName((previous) =>
        String(
          (typeof updater === 'function' ? updater(previous) : updater) ?? ''
        )
      )
      setPagination((previous) => ({ ...previous, pageIndex: 0 }))
    },
    pagination,
    onPaginationChange: (updater) => {
      setPagination((previous) => {
        const next = typeof updater === 'function' ? updater(previous) : updater
        return {
          ...next,
          pageIndex: next.pageSize === previous.pageSize ? next.pageIndex : 0,
        }
      })
    },
    ensurePageInRange: (pages) => {
      if (!query.data) return
      setPagination((previous) => {
        const pageIndex = Math.max(0, pages - 1)
        return previous.pageIndex > pageIndex
          ? { ...previous, pageIndex }
          : previous
      })
    },
    manualFiltering: true,
    manualPagination: true,
    manualSorting: true,
    enableSorting: false,
    enableRowSelection: canDelete,
    rowSelection: selection.rows,
    onRowSelectionChange: (updater) =>
      setSelection((previous) => ({
        ...previous,
        rows: typeof updater === 'function' ? updater(previous.rows) : updater,
      })),
    getRowId: (asset) => asset.Id,
  })
  const searchToolbarProps = {
    ...toolbarProps,
    searchPlaceholder: t('Filter by name...'),
    searchDebounceMs: 300,
  }
  if (!group || (query.isError && !query.data)) {
    return (
      <div className='flex h-full min-h-0 flex-col gap-3'>
        <DataTableToolbar table={table} {...searchToolbarProps} />
        <div className='min-h-0 flex-1 overflow-auto'>
          {!group ? (
            emptyState
          ) : (
            <ErrorState
              description={getServerErrorMessage(query.error)}
              onRetry={() => void query.refetch()}
            />
          )}
        </div>
      </div>
    )
  }
  return (
    <div
      ref={listRegion}
      role='region'
      aria-label={t('Asset library')}
      tabIndex={-1}
      className='focus-visible:ring-ring h-full min-h-0 rounded-xl focus-visible:ring-2 focus-visible:outline-none'
    >
      <DataTablePage
        table={table}
        columns={columns}
        isLoading={query.isPending}
        isFetching={query.isFetching}
        toolbarProps={searchToolbarProps}
        emptyTitle={t('No assets')}
        mobileProps={{ enableRowSelection: canDelete }}
        afterTable={
          <>
            {canDelete && (
              <DataTableBulkActions
                table={table}
                entityName={t('Asset')}
                placement='inline'
              >
                <Button
                  ref={batchDeleteTrigger}
                  variant='destructive'
                  disabled={remove.isPending || Boolean(batchDeleting)}
                  onClick={() =>
                    setBatchDeleting({
                      assets: table.getSelectedRowModel().rows.map((row) => ({
                        Id: row.original.Id,
                        Name: row.original.Name,
                      })),
                      project: group.ProjectName,
                      scope: { ...scope },
                    })
                  }
                >
                  <Trash2 className='size-4' />
                  {t('Delete selected assets')}
                </Button>
              </DataTableBulkActions>
            )}
            {totalCount > maxPages * pagination.pageSize && (
              <p className='text-muted-foreground text-sm'>
                {t(
                  'Only the first {{limit}} assets can be browsed. Filter by name to narrow the results.',
                  {
                    limit: formatNumber(
                      maxPages * pagination.pageSize,
                      toIntlLocale(i18n.resolvedLanguage || i18n.language)
                    ),
                  }
                )}
              </p>
            )}
            {query.isError && (
              <p role='alert' className='text-destructive text-sm'>
                {getServerErrorMessage(query.error)}
              </p>
            )}
          </>
        }
      />
      <ConfirmDialog
        open={Boolean(deleting)}
        onOpenChange={(open) => !open && !remove.isPending && setDeleting(null)}
        title={t('Delete asset')}
        desc={t('Delete {{name}}? This asset URI will no longer work.', {
          name: deleting?.asset.Name,
        })}
        confirmText={t('Delete')}
        destructive
        isLoading={remove.isPending}
        disabled={frozen}
        handleConfirm={() => deleting && remove.mutate(deleting)}
      >
        {remove.isError && (
          <p role='alert' className='text-destructive text-sm'>
            {getServerErrorMessage(remove.error)}
          </p>
        )}
      </ConfirmDialog>
      {batchDeleting && (
        <AssetBatchDeleteDialog
          target={batchDeleting}
          disabled={!canDelete}
          finalFocus={(completed) =>
            completed
              ? listRegion.current
              : (batchDeleteTrigger.current ?? listRegion.current)
          }
          onClose={() => setBatchDeleting(null)}
          onDeleted={(ids) =>
            setSelection((previous) => ({
              ...previous,
              rows: Object.fromEntries(
                Object.entries(previous.rows).filter(
                  ([id]) => !ids.includes(id)
                )
              ),
            }))
          }
        />
      )}
      {replicas && canReadChannel && (
        <AssetReplicasDialog
          asset={replicas}
          scope={{ userId: replicas.UserId ?? scope.userId }}
          readOnly={frozen}
          onClose={() => setReplicas(null)}
        />
      )}
      {preview && (
        <Dialog
          open
          onOpenChange={(open) => !open && setPreview(null)}
          title={preview.Name}
          contentHeight='auto'
        >
          {preview.AssetType === 'Image' && (
            <img
              src={preview.URL}
              alt={preview.Name}
              className='max-h-[65vh] w-full object-contain'
              referrerPolicy='no-referrer'
            />
          )}
          {preview.AssetType === 'Video' && (
            <video
              src={preview.URL}
              controls
              preload='metadata'
              aria-label={preview.Name}
              className='max-h-[65vh] w-full'
            />
          )}
          {preview.AssetType === 'Audio' && (
            <audio
              src={preview.URL}
              controls
              preload='metadata'
              aria-label={preview.Name}
              className='w-full'
            />
          )}
        </Dialog>
      )}
    </div>
  )
}
