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
import {
  useInfiniteQuery,
  useMutation,
  useQueryClient,
} from '@tanstack/react-query'
import { Plus, RefreshCw } from 'lucide-react'
import { useId, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { CopyButton } from '@/components/copy-button'
import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import { NativeSelect } from '@/components/ui/native-select'
import { getServerErrorMessage } from '@/lib/server-error-message'

import { assetQueryKeys, executeAssetAction } from '../api'
import type { AssetGroup, AssetScope } from '../types'
import { AssetCreateDialog } from './asset-create-dialog'
import { AssetGroupDialog } from './asset-group-dialog'
import { AssetListTable } from './asset-list-table'

export function AssetBrowser({
  project,
  scope,
  admin,
}: {
  project: string
  scope: AssetScope
  admin: boolean
}) {
  const { t } = useTranslation()
  const id = useId()
  const client = useQueryClient()
  const [groupId, setGroupId] = useState('')
  const [groupDialog, setGroupDialog] = useState<'create' | 'rename' | null>(
    null
  )
  const [adding, setAdding] = useState(false)
  const [deleting, setDeleting] = useState(false)
  const groups = useInfiniteQuery({
    queryKey: [...assetQueryKeys.all, 'groups', project, scope],
    initialPageParam: '',
    queryFn: ({ signal, pageParam }) =>
      executeAssetAction<{ Items: AssetGroup[]; NextToken?: string }>(
        'ListAssetGroups',
        {
          ProjectName: project,
          Filter: { GroupType: 'AIGC' },
          MaxResults: 100,
          ...(pageParam ? { NextToken: pageParam } : {}),
        },
        scope,
        signal
      ),
    getNextPageParam: (page) => page.NextToken || undefined,
    meta: { errorToast: false },
  })
  const items = groups.data?.pages.flatMap((page) => page.Items) ?? []
  const group = items.find((item) => item.Id === groupId) ?? items[0]
  const remove = useMutation({
    mutationFn: (target: AssetGroup) =>
      executeAssetAction(
        'DeleteAssetGroup',
        { ProjectName: project, Id: target.Id },
        scope
      ),
    onSuccess: () => {
      setDeleting(false)
      void client.invalidateQueries({ queryKey: assetQueryKeys.all })
      toast.success(t('Deleted successfully'))
    },
    meta: { errorToast: false },
  })
  if (groups.isPending) return <LoadingState />
  if (groups.isError && !groups.data) {
    return (
      <ErrorState
        description={getServerErrorMessage(groups.error)}
        onRetry={() => void groups.refetch()}
      />
    )
  }
  return (
    <div className='space-y-4'>
      {scope.allUsers && (
        <p className='text-muted-foreground text-sm'>
          {t('Select a user ID to create or change assets.')}
        </p>
      )}
      <div className='flex flex-wrap items-end gap-2'>
        <div className='min-w-0 space-y-1.5'>
          <Label htmlFor={id}>{t('Asset group')}</Label>
          <NativeSelect
            id={id}
            className='w-full max-w-sm'
            value={group?.Id ?? ''}
            onChange={(event) => setGroupId(event.target.value)}
          >
            {!items.length && <option value=''>{t('No asset groups')}</option>}
            {items.map((item) => (
              <option value={item.Id} key={item.Id}>
                {scope.allUsers ? `${item.Name} (${item.UserId})` : item.Name}
              </option>
            ))}
          </NativeSelect>
        </div>
        {groups.hasNextPage && (
          <Button
            variant='outline'
            disabled={groups.isFetchingNextPage}
            onClick={() => void groups.fetchNextPage()}
          >
            {t('Load more')}
          </Button>
        )}
        <Button
          variant='outline'
          disabled={scope.allUsers}
          onClick={() => setGroupDialog('create')}
        >
          <Plus className='size-4' />
          {t('Create asset group')}
        </Button>
        <Button
          variant='outline'
          disabled={!group || scope.allUsers}
          onClick={() => setGroupDialog('rename')}
        >
          {t('Rename')}
        </Button>
        <Button
          variant='outline'
          disabled={!group || scope.allUsers}
          onClick={() => {
            remove.reset()
            setDeleting(true)
          }}
        >
          {t('Delete group')}
        </Button>
        <Button
          disabled={!group || scope.allUsers}
          onClick={() => setAdding(true)}
        >
          <Plus className='size-4' />
          {t('Add asset')}
        </Button>
        <Button
          variant='ghost'
          size='icon'
          aria-label={t('Refresh')}
          disabled={groups.isFetching}
          onClick={() =>
            void client.invalidateQueries({ queryKey: assetQueryKeys.all })
          }
        >
          <RefreshCw className='size-4' />
        </Button>
      </div>
      {groups.isError && (
        <ErrorState
          className='min-h-0 p-3'
          description={getServerErrorMessage(groups.error)}
          onRetry={() =>
            void (groups.isFetchNextPageError
              ? groups.fetchNextPage()
              : groups.refetch())
          }
        />
      )}
      {group && (
        <>
          <div className='text-muted-foreground flex min-w-0 items-center gap-1 text-xs'>
            <span className='truncate'>{group.Id}</span>
            <CopyButton value={group.Id} />
          </div>
          <AssetListTable
            key={group.Id}
            group={group}
            scope={scope}
            admin={admin}
          />
        </>
      )}
      {groupDialog && (
        <AssetGroupDialog
          group={groupDialog === 'rename' ? group : undefined}
          project={project}
          scope={scope}
          onClose={() => setGroupDialog(null)}
        />
      )}
      {adding && group && (
        <AssetCreateDialog
          group={group}
          scope={scope}
          onClose={() => setAdding(false)}
        />
      )}
      <ConfirmDialog
        open={deleting}
        onOpenChange={(open) => !remove.isPending && setDeleting(open)}
        title={t('Delete asset group')}
        desc={t(
          'Delete {{name}} and all assets in this group? Their asset URIs will no longer work.',
          { name: group?.Name }
        )}
        destructive
        confirmText={t('Delete')}
        isLoading={remove.isPending}
        handleConfirm={() => group && remove.mutate(group)}
      >
        {remove.isError && (
          <p role='alert' className='text-destructive text-sm'>
            {getServerErrorMessage(remove.error)}
          </p>
        )}
      </ConfirmDialog>
    </div>
  )
}
