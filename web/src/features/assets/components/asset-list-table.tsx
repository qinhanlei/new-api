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
import type { CellContext, ColumnDef } from '@tanstack/react-table'
import { Eye, Trash2 } from 'lucide-react'
import { useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { CopyButton } from '@/components/copy-button'
import {
  DataTablePage,
  DataTableToolbar,
  useDataTable,
  type DataTablePageProps,
} from '@/components/data-table'
import { Dialog } from '@/components/dialog'
import { ErrorState } from '@/components/error-state'
import { Button } from '@/components/ui/button'
import {
  ADMIN_PERMISSION_ACTIONS,
  ADMIN_PERMISSION_RESOURCES,
  hasPermission,
} from '@/lib/admin-permissions'
import { getServerErrorMessage } from '@/lib/server-error-message'
import { useAuthStore } from '@/stores/auth-store'

import { assetQueryKeys, executeAssetAction } from '../api'
import type { Asset, AssetGroup, AssetList, AssetScope } from '../types'
import { AssetReplicasDialog } from './asset-replicas-dialog'
import { AssetStatus } from './asset-status'

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
  const { t } = useTranslation()
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
  const [deleting, setDeleting] = useState<Asset | null>(null)
  const [replicas, setReplicas] = useState<Asset | null>(null)
  const [preview, setPreview] = useState<Asset | null>(null)
  const query = useQuery({
    queryKey: [...assetQueryKeys.all, 'list', group?.Id, scope, pagination],
    enabled: Boolean(group),
    queryFn: ({ signal }) => {
      if (!group) return { Items: [], TotalCount: 0 }
      return executeAssetAction<AssetList<Asset>>(
        'ListAssets',
        {
          ProjectName: group.ProjectName,
          Filter: { GroupType: 'AIGC', GroupIds: [group.Id] },
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
  const remove = useMutation({
    mutationFn: (asset: Asset) =>
      executeAssetAction(
        'DeleteAsset',
        { ProjectName: asset.ProjectName, Id: asset.Id },
        scope
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
  const columns: ColumnDef<Asset>[] = [
    {
      id: 'name',
      accessorKey: 'Name',
      header: t('Name'),
      cell: ({ row: { original: asset } }) => (
        <div className='space-y-1'>
          <p>{asset.Name}</p>
          <div className='text-muted-foreground flex items-center gap-1 text-xs'>
            <code className='max-w-64 truncate'>asset://{asset.Id}</code>
            <CopyButton
              value={`asset://${asset.Id}`}
              tooltip={t('Copy asset URI')}
            />
          </div>
        </div>
      ),
    },
    {
      id: 'type',
      accessorKey: 'AssetType',
      header: t('Asset type'),
      cell: ({ row: { original: asset } }) => t(asset.AssetType),
    },
    ...(scope.allUsers
      ? [
          {
            id: 'owner',
            header: t('User ID'),
            cell: ({ row: { original: asset } }: CellContext<Asset, unknown>) =>
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
      id: 'actions',
      header: t('Actions'),
      cell: ({ row: { original: asset } }) => (
        <div className='flex items-center gap-1'>
          <Button
            variant='ghost'
            size='icon-sm'
            aria-label={t('Preview {{name}}', { name: asset.Name })}
            disabled={!/^https?:\/\//i.test(asset.URL)}
            onClick={() => setPreview(asset)}
          >
            <Eye className='size-4' />
          </Button>
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
            disabled={scope.allUsers}
            onClick={() => {
              remove.reset()
              setDeleting(asset)
            }}
          >
            <Trash2 className='size-4' />
          </Button>
        </div>
      ),
    },
  ]
  const { table } = useDataTable({
    data: query.data?.Items ?? [],
    columns,
    columnFilters: [],
    totalCount: query.data?.TotalCount ?? 0,
    pagination,
    onPaginationChange: setPagination,
    manualPagination: true,
    manualSorting: true,
    enableSorting: false,
    enableRowSelection: false,
    getRowId: (asset) => asset.Id,
  })
  if (!group || (query.isError && !query.data)) {
    return (
      <div className='flex h-full min-h-0 flex-col gap-3'>
        <DataTableToolbar table={table} {...toolbarProps} />
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
    <>
      <DataTablePage
        table={table}
        columns={columns}
        isLoading={query.isPending}
        isFetching={query.isFetching}
        toolbarProps={toolbarProps}
        emptyTitle={t('No assets')}
        afterTable={
          query.isError && (
            <p role='alert' className='text-destructive text-sm'>
              {getServerErrorMessage(query.error)}
            </p>
          )
        }
      />
      <ConfirmDialog
        open={Boolean(deleting)}
        onOpenChange={(open) => !open && !remove.isPending && setDeleting(null)}
        title={t('Delete asset')}
        desc={t('Delete {{name}}? This asset URI will no longer work.', {
          name: deleting?.Name,
        })}
        confirmText={t('Delete')}
        destructive
        isLoading={remove.isPending}
        handleConfirm={() => deleting && remove.mutate(deleting)}
      >
        {remove.isError && (
          <p role='alert' className='text-destructive text-sm'>
            {getServerErrorMessage(remove.error)}
          </p>
        )}
      </ConfirmDialog>
      {replicas && canReadChannel && (
        <AssetReplicasDialog
          asset={replicas}
          scope={{ userId: replicas.UserId ?? scope.userId }}
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
    </>
  )
}
