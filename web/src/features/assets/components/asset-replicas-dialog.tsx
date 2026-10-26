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
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'

import { StaticDataTable } from '@/components/data-table'
import { Dialog } from '@/components/dialog'
import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { getServerErrorMessage } from '@/lib/server-error-message'

import { assetQueryKeys, getAssetReplicas } from '../api'
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
  const query = useQuery({
    queryKey: [...assetQueryKeys.all, 'replicas', asset.Id, scope.userId],
    queryFn: ({ signal }) => getAssetReplicas(asset.Id, scope, signal),
    refetchInterval: 10_000,
    meta: { errorToast: false },
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
          getRowKey={(replica) => replica.channel_id}
          emptyContent={t(
            'Replicas are created when an asset is first used on a channel.'
          )}
          columns={[
            {
              id: 'channel',
              header: t('Channel'),
              cell: (replica) =>
                replica.channel_name || String(replica.channel_id),
            },
            {
              id: 'status',
              header: t('Status'),
              cell: (replica) => <AssetStatus status={replica.status} />,
            },
            {
              id: 'error',
              header: t('Error'),
              cell: (replica) => (
                <span className='block max-w-sm break-words whitespace-normal'>
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
