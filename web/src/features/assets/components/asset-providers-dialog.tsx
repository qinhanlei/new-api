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

import { Dialog } from '@/components/dialog'
import { EmptyState } from '@/components/empty-state'
import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'

import { listAssetProviders } from '../api'
import type { Asset } from '../types'
import { AssetStatus } from './asset-status'

export function AssetProvidersDialog(props: {
  asset: Asset
  onClose: () => void
}) {
  const { t } = useTranslation()
  const query = useQuery({
    queryKey: ['asset-providers', props.asset.id],
    queryFn: () => listAssetProviders(props.asset.id),
    refetchInterval: 5000,
  })
  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open) props.onClose()
      }}
      title={t('Provider replicas')}
      description={props.asset.name}
    >
      {query.isLoading && <LoadingState />}
      {query.isError && (
        <ErrorState
          onRetry={() => {
            void query.refetch()
          }}
        />
      )}
      {query.isSuccess && query.data.length === 0 && (
        <EmptyState
          title={t('No provider replicas')}
          description={t(
            'Replicas are created when a video request uses this asset.'
          )}
        />
      )}
      {query.isSuccess && (
        <ul className='space-y-3'>
          {query.data.map((provider) => (
            <li
              key={provider.channel_id}
              className='space-y-2 rounded-lg border p-3'
            >
              <div className='flex flex-wrap items-center justify-between gap-2'>
                <span className='min-w-0 font-medium break-words'>
                  {provider.name}
                </span>
                <AssetStatus status={provider.status} />
              </div>
              {provider.error && (
                <p
                  role='status'
                  className='text-destructive text-sm break-words'
                >
                  {t(provider.error)}
                </p>
              )}
            </li>
          ))}
        </ul>
      )}
    </Dialog>
  )
}
