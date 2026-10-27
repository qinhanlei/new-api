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
import { useId } from 'react'
import { useTranslation } from 'react-i18next'

import { ErrorState } from '@/components/error-state'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { NativeSelect } from '@/components/ui/native-select'
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

export function AssetChannelSelect(props: {
  channel: Channel | null
  onChange: (channel: Channel) => void
  search: AssetChannelSearch
  onSearchChange: (search: AssetChannelSearch) => void
  disabled?: boolean
}) {
  const { t } = useTranslation()
  const id = useId()
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
  const options =
    props.channel && !items.some((item) => item.id === props.channel?.id)
      ? [props.channel, ...items]
      : items
  return (
    <div className='space-y-2'>
      <form
        className='flex gap-2'
        onSubmit={(event) => {
          event.preventDefault()
          if (!props.disabled) {
            props.onSearchChange({
              ...props.search,
              keyword: props.search.draft.trim(),
              page: 1,
            })
          }
        }}
      >
        <Input
          type='search'
          aria-label={t('Search channels')}
          placeholder={t('Name or ID')}
          value={props.search.draft}
          onChange={(event) =>
            props.onSearchChange({ ...props.search, draft: event.target.value })
          }
          disabled={props.disabled}
          className='min-w-0 flex-1'
        />
        <Button
          type='submit'
          variant='outline'
          disabled={props.disabled || query.isFetching}
        >
          {t('Search')}
        </Button>
      </form>
      <div className='flex min-w-0 items-center gap-2'>
        <Label htmlFor={id} className='shrink-0'>
          {t('Channel')}
        </Label>
        <NativeSelect
          id={id}
          value={props.channel?.id ?? ''}
          className='w-full min-w-0'
          disabled={props.disabled || query.isPending}
          onChange={(event) => {
            const selected = options.find(
              (item) => item.id === Number(event.target.value)
            )
            if (selected) props.onChange(selected)
          }}
        >
          <option value='' disabled>
            {query.isPending ? t('Loading...') : t('Select a channel')}
          </option>
          {options.map((item) => (
            <option key={item.id} value={item.id}>
              {item.name} (#{item.id})
            </option>
          ))}
        </NativeSelect>
        {((query.data?.total ?? 0) > 20 || page > 1) && (
          <>
            <Button
              size='icon'
              variant='outline'
              aria-label={t('Previous page')}
              disabled={props.disabled || query.isFetching || page === 1}
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
              disabled={
                props.disabled ||
                query.isFetching ||
                page * 20 >= (query.data?.total ?? 0)
              }
              onClick={() =>
                props.onSearchChange({ ...props.search, page: page + 1 })
              }
            >
              <ChevronRight className='size-4' />
            </Button>
          </>
        )}
      </div>
      {query.isError && (
        <ErrorState
          className='min-h-0 p-3'
          description={getServerErrorMessage(query.error)}
          onRetry={() => void query.refetch()}
        />
      )}
      {query.isSuccess && items.length === 0 && (
        <p className='text-muted-foreground text-sm'>
          {t('No Channels Found')}
        </p>
      )}
    </div>
  )
}
