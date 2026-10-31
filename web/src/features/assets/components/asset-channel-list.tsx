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
import { ChevronLeft, ChevronRight } from 'lucide-react'
import { useEffect, useRef } from 'react'
import { useTranslation } from 'react-i18next'

import { StaticDataTable } from '@/components/data-table'
import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { TableId } from '@/components/table-id'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { getChannels, searchChannels } from '@/features/channels/api'
import type { Channel } from '@/features/channels/types'
import {
  getServerErrorMessage,
  requireServerSuccess,
} from '@/lib/server-error-message'

export type AssetChannelSearch = {
  draft: string
  keyword: string
  page: number
}

export type AssetChannelListPosition = {
  channelId: number
  scrollTop: number
}

export function AssetChannelList(props: {
  onEdit: (channel: Channel, scrollTop: number) => void
  search: AssetChannelSearch
  onSearchChange: (search: AssetChannelSearch) => void
  position: AssetChannelListPosition | null
}) {
  const { t } = useTranslation()
  const container = useRef<HTMLDivElement>(null)
  const searchInput = useRef<HTMLInputElement>(null)
  const editButton = useRef<HTMLButtonElement>(null)
  const restored = useRef(false)
  const { keyword, page } = props.search
  const params = { p: page, page_size: 20, tag_mode: false, type: 45 }
  const query = useQuery({
    queryKey: ['asset-channel-options', { ...params, keyword }],
    queryFn: async () => {
      const result = keyword
        ? await searchChannels({ ...params, keyword })
        : await getChannels(params)
      return requireServerSuccess(result).data
    },
    refetchOnWindowFocus: false,
    meta: { errorToast: false },
  })
  const items = query.data?.items ?? []
  useEffect(() => {
    if (!props.position || restored.current || query.isFetching) return
    restored.current = true
    if (container.current) {
      container.current.scrollTop = props.position.scrollTop
    }
    const target = editButton.current ?? searchInput.current
    target?.focus({ preventScroll: true })
  }, [props.position, query.isFetching])

  return (
    <div className='space-y-3'>
      <form
        className='flex gap-2'
        onSubmit={(event) => {
          event.preventDefault()
          props.onSearchChange({
            ...props.search,
            keyword: props.search.draft.trim(),
            page: 1,
          })
        }}
      >
        <Input
          ref={searchInput}
          type='search'
          aria-label={t('Search channels')}
          placeholder={t('Name or ID')}
          value={props.search.draft}
          onChange={(event) =>
            props.onSearchChange({ ...props.search, draft: event.target.value })
          }
          className='min-w-0 flex-1'
        />
        <Button type='submit' variant='outline' disabled={query.isFetching}>
          {t('Search')}
        </Button>
      </form>
      {query.isPending && <LoadingState />}
      {query.isSuccess && (
        <StaticDataTable
          data={items}
          getRowKey={(item) => item.id}
          className='max-h-[50dvh] overflow-y-auto'
          containerProps={{
            ref: container,
            role: 'region',
            'aria-label': t('Channels'),
          }}
          tableProps={{ withContainer: false }}
          tableClassName='table-fixed'
          headerRowClassName='bg-background sticky top-0 z-10'
          emptyContent={t('No Channels Found')}
          columns={[
            {
              id: 'name',
              header: t('Channel'),
              cell: (item) => item.name,
            },
            {
              id: 'id',
              header: t('ID'),
              className: 'w-20',
              cell: (item) => <TableId value={item.id} />,
            },
            {
              id: 'actions',
              header: t('Actions'),
              className: 'w-24 text-right',
              cellClassName: 'text-right',
              cell: (item) => (
                <Button
                  ref={
                    item.id === props.position?.channelId
                      ? editButton
                      : undefined
                  }
                  variant='outline'
                  size='sm'
                  className='h-auto min-h-8 max-w-full whitespace-normal'
                  aria-label={t('Edit {{title}}', {
                    title: `${item.name} (#${item.id})`,
                  })}
                  onClick={() =>
                    props.onEdit(item, container.current?.scrollTop ?? 0)
                  }
                >
                  <span className='min-w-0 break-words'>{t('Edit')}</span>
                </Button>
              ),
            },
          ]}
        />
      )}
      {((query.data?.total ?? 0) > 20 || page > 1) && (
        <div className='flex justify-end gap-2'>
          <Button
            size='icon'
            variant='outline'
            aria-label={t('Previous page')}
            disabled={query.isFetching || page === 1}
            onClick={() =>
              props.onSearchChange({ ...props.search, page: page - 1 })
            }
          >
            <ChevronLeft className='size-4' />
          </Button>
          <Button
            size='icon'
            variant='outline'
            aria-label={t('Next page')}
            disabled={query.isFetching || page * 20 >= (query.data?.total ?? 0)}
            onClick={() =>
              props.onSearchChange({ ...props.search, page: page + 1 })
            }
          >
            <ChevronRight className='size-4' />
          </Button>
        </div>
      )}
      {query.isError && (
        <ErrorState
          className='min-h-0 p-3'
          description={getServerErrorMessage(query.error)}
          onRetry={() => void query.refetch()}
        />
      )}
    </div>
  )
}
