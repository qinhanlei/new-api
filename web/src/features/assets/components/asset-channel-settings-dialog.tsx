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
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Dialog } from '@/components/dialog'
import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { Button } from '@/components/ui/button'
import type { Channel } from '@/features/channels/types'
import { getServerErrorMessage } from '@/lib/server-error-message'

import { assetQueryKeys, getAssetChannel } from '../api'
import {
  AssetChannelSelect,
  type AssetChannelSearch,
} from './asset-channel-select'
import { ChannelAssetsForm } from './channel-assets-form'

export function AssetChannelSettingsDialog(props: { onClose: () => void }) {
  const { t } = useTranslation()
  const [channel, setChannel] = useState<Channel | null>(null)
  const [search, setSearch] = useState<AssetChannelSearch>({
    draft: '',
    keyword: '',
    page: 1,
  })
  const query = useQuery({
    queryKey: assetQueryKeys.channel(channel?.id ?? 0),
    queryFn: ({ signal }) => getAssetChannel(channel?.id ?? 0, signal),
    enabled: channel !== null,
    refetchOnWindowFocus: false,
    meta: { errorToast: false },
  })
  if (channel && query.data) {
    return (
      <ChannelAssetsForm
        key={channel.id}
        channel={channel}
        config={query.data}
        onChannelChange={setChannel}
        search={search}
        onSearchChange={setSearch}
        onClose={props.onClose}
      />
    )
  }
  return (
    <Dialog
      open
      onOpenChange={(open) => !open && props.onClose()}
      title={t('Channel settings')}
      footer={
        <Button variant='outline' onClick={props.onClose}>
          {t('Close')}
        </Button>
      }
    >
      <AssetChannelSelect
        channel={channel}
        onChange={setChannel}
        search={search}
        onSearchChange={setSearch}
      />
      {channel && query.isPending && <LoadingState />}
      {query.isError && (
        <ErrorState
          description={getServerErrorMessage(query.error)}
          onRetry={() => void query.refetch()}
        />
      )}
    </Dialog>
  )
}
