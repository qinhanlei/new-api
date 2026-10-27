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
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { StaticDataTable } from '@/components/data-table'
import { Dialog } from '@/components/dialog'
import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { Button } from '@/components/ui/button'
import {
  ADMIN_PERMISSION_ACTIONS,
  ADMIN_PERMISSION_RESOURCES,
  hasPermission,
} from '@/lib/admin-permissions'
import { handleServerError } from '@/lib/handle-server-error'
import { ROLE } from '@/lib/roles'
import { getServerErrorMessage } from '@/lib/server-error-message'
import { useAuthStore } from '@/stores/auth-store'

import { assetQueryKeys, getAssetReplicas, retryAssetReplica } from '../api'
import type { Asset, AssetScope } from '../types'
import { AssetStatus } from './asset-status'

export function AssetReplicasDialog({
  asset,
  scope,
  onClose,
}: {
  asset: Asset
  scope: AssetScope
  onClose: () => void
}) {
  const { t } = useTranslation()
  const client = useQueryClient()
  const currentUser = useAuthStore((state) => state.auth.user)
  const canWriteChannel =
    currentUser &&
    currentUser.role >= ROLE.ADMIN &&
    hasPermission(
      currentUser,
      ADMIN_PERMISSION_RESOURCES.CHANNEL,
      ADMIN_PERMISSION_ACTIONS.WRITE
    )
  const query = useQuery({
    queryKey: [...assetQueryKeys.all, 'replicas', asset.Id, scope.userId],
    queryFn: ({ signal }) => getAssetReplicas(asset.Id, scope, signal),
    refetchInterval: 10_000,
    meta: { errorToast: false },
  })
  const retry = useMutation({
    mutationFn: (channelId: number) =>
      retryAssetReplica(asset.Id, channelId, scope),
    onSuccess: async () => {
      await client.invalidateQueries({ queryKey: assetQueryKeys.all })
      toast.success(t('Asset synchronization queued'))
    },
    onError: (error) => handleServerError(error),
  })
  return (
    <Dialog
      open
      onOpenChange={(open) => !open && onClose()}
      title={t('Channel replicas')}
      description={asset.Name}
      contentHeight='auto'
    >
      {query.isPending && <LoadingState />}
      {query.isError && (
        <ErrorState
          description={getServerErrorMessage(query.error)}
          onRetry={() => void query.refetch()}
        />
      )}
      {query.data && (
        <StaticDataTable
          data={query.data}
          tableClassName='table-fixed'
          getRowKey={(replica) => replica.channel_id}
          emptyContent={t(
            'Replicas are created when an asset is first used on a channel.'
          )}
          columns={[
            {
              id: 'channel',
              header: t('Channel'),
              className: 'w-1/4',
              cellClassName: 'align-top',
              cell: (replica) =>
                replica.channel_name || String(replica.channel_id),
            },
            {
              id: 'status',
              header: t('Status'),
              className: 'w-28',
              cellClassName: 'align-top',
              cell: (replica) => {
                const pending =
                  retry.isPending && retry.variables === replica.channel_id
                return (
                  <div className='flex flex-col items-start gap-1'>
                    <AssetStatus status={replica.status} />
                    {canWriteChannel && replica.can_retry && (
                      <Button
                        variant='outline'
                        size='sm'
                        className='h-auto min-h-7 max-w-full whitespace-normal'
                        disabled={retry.isPending}
                        aria-label={t('Resync {{name}}', {
                          name:
                            replica.channel_name || String(replica.channel_id),
                        })}
                        onClick={() => retry.mutate(replica.channel_id)}
                      >
                        <span className='min-w-0 break-words'>
                          {pending ? t('Syncing...') : t('Resync')}
                        </span>
                      </Button>
                    )}
                  </div>
                )
              },
            },
            {
              id: 'error',
              header: t('Error'),
              cellClassName: 'align-top whitespace-normal',
              cell: (replica) => (
                <span className='block break-words whitespace-normal'>
                  {[replica.error_code, t(replica.error_message)]
                    .filter(Boolean)
                    .join(': ') || '—'}
                </span>
              ),
            },
          ]}
        />
      )}
    </Dialog>
  )
}
