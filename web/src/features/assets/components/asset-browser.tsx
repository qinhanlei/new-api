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
import { Copy, MoreHorizontal, Plus, RefreshCw } from 'lucide-react'
import {
  lazy,
  Suspense,
  useEffect,
  useId,
  useState,
  type ReactNode,
} from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { EmptyState } from '@/components/empty-state'
import { ErrorState } from '@/components/error-state'
import { SectionPageLayout } from '@/components/layout'
import { LoadingState } from '@/components/loading-state'
import { Button } from '@/components/ui/button'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuGroup,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { Label } from '@/components/ui/label'
import { NativeSelect } from '@/components/ui/native-select'
import { useCopyToClipboard } from '@/hooks/use-copy-to-clipboard'
import { getServerErrorMessage } from '@/lib/server-error-message'

import { assetQueryKeys, executeAssetAction } from '../api'
import type { AssetGroup, AssetGroupType, AssetScope } from '../types'
import { AssetCreateDialog } from './asset-create-dialog'
import { AssetGroupDialog } from './asset-group-dialog'
import { AssetGroupLifecycle } from './asset-group-lifecycle'
import { AssetListTable } from './asset-list-table'

type AssetGroupTarget = { group: AssetGroup; scope: AssetScope }

const AssetValidationDialog = lazy(() =>
  import('./asset-validation-dialog').then((module) => ({
    default: module.AssetValidationDialog,
  }))
)

export function AssetBrowser({
  project,
  scope,
  admin,
  settingsActions,
  filterControls,
  filterActions,
}: {
  project: string
  scope: AssetScope
  admin: boolean
  settingsActions?: ReactNode
  filterControls?: ReactNode
  filterActions?: ReactNode
}) {
  const { t } = useTranslation()
  const id = useId()
  const client = useQueryClient()
  const { copyToClipboard } = useCopyToClipboard()
  const [groupId, setGroupId] = useState('')
  const [groupType, setGroupType] = useState<AssetGroupType>('AIGC')
  const [validating, setValidating] = useState(false)
  const [groupDialog, setGroupDialog] = useState<{
    group?: AssetGroup
    project: string
    scope: AssetScope
  } | null>(null)
  const [adding, setAdding] = useState<AssetGroupTarget | null>(null)
  const [deleting, setDeleting] = useState<AssetGroupTarget | null>(null)
  const groups = useInfiniteQuery({
    queryKey: [...assetQueryKeys.all, 'groups', groupType, project, scope],
    initialPageParam: '',
    queryFn: ({ signal, pageParam }) =>
      executeAssetAction<{ Items: AssetGroup[]; NextToken?: string }>(
        'ListAssetGroups',
        {
          ProjectName: project,
          Filter: { GroupType: groupType },
          MaxResults: 100,
          ...(pageParam ? { NextToken: pageParam } : {}),
        },
        scope,
        signal
      ),
    getNextPageParam: (page) => page.NextToken || undefined,
    refetchInterval: groupType === 'LivenessFace' ? 10_000 : false,
    meta: { errorToast: false },
  })
  const items = groups.data?.pages.flatMap((page) => page.Items) ?? []
  const group = items.find((item) => item.Id === groupId) ?? items[0]
  const frozen = Boolean(group?.Status && group.Status !== 'active')
  const addingGroup = items.find((item) => item.Id === adding?.group.Id)
  const canAddToGroup = Boolean(
    adding &&
    (adding.group.GroupType !== 'LivenessFace' ||
      (addingGroup && (!addingGroup.Status || addingGroup.Status === 'active')))
  )
  useEffect(() => {
    if (adding && !canAddToGroup) setAdding(null)
  }, [adding, canAddToGroup])
  const remove = useMutation({
    mutationFn: (target: AssetGroupTarget) =>
      executeAssetAction<{ Status?: AssetGroup['Status'] }>(
        'DeleteAssetGroup',
        { ProjectName: target.group.ProjectName, Id: target.group.Id },
        target.scope
      ),
    onSuccess: (result) => {
      setDeleting(null)
      void client.invalidateQueries({ queryKey: assetQueryKeys.all })
      toast.success(
        result.Status && result.Status !== 'deleted'
          ? t('Group deletion requested')
          : t('Deleted successfully')
      )
    },
    meta: { errorToast: false },
  })
  let emptyState = (
    <EmptyState
      title={t('No asset groups')}
      action={
        !scope.allUsers && (
          <Button
            onClick={() =>
              groupType === 'LivenessFace'
                ? setValidating(true)
                : setGroupDialog({ project, scope: { ...scope } })
            }
          >
            {groupType === 'LivenessFace'
              ? t('Verify a person')
              : t('Create asset group')}
          </Button>
        )
      }
    />
  )
  if (groups.isPending) {
    emptyState = <LoadingState />
  } else if (groups.isError) {
    emptyState = (
      <ErrorState
        description={getServerErrorMessage(groups.error)}
        onRetry={() => void groups.refetch()}
      />
    )
  }
  return (
    <>
      <SectionPageLayout fixedContent>
        <SectionPageLayout.Title>{t('Asset library')}</SectionPageLayout.Title>
        <SectionPageLayout.Actions>
          {settingsActions}
          <Button
            disabled={!group || scope.allUsers || frozen}
            onClick={() => group && setAdding({ group, scope: { ...scope } })}
          >
            <Plus className='size-4' />
            {t('Add asset')}
          </Button>
        </SectionPageLayout.Actions>
        <SectionPageLayout.Content>
          <div className='flex h-full min-h-0 flex-col gap-3'>
            {groups.isError && groups.data && (
              <ErrorState
                className='min-h-0 shrink-0 p-3'
                description={getServerErrorMessage(groups.error)}
                onRetry={() =>
                  void (groups.isFetchNextPageError
                    ? groups.fetchNextPage()
                    : groups.refetch())
                }
              />
            )}
            {group?.GroupType === 'LivenessFace' && (
              <AssetGroupLifecycle
                group={group}
                scope={scope}
                onRetryDelete={() => {
                  remove.reset()
                  setDeleting({ group, scope: { ...scope } })
                }}
              />
            )}
            <div className='min-h-0 flex-1'>
              <AssetListTable
                key={`${groupType}:${group?.Id ?? 'empty'}`}
                group={group}
                scope={scope}
                admin={admin}
                toolbarProps={{
                  hideViewOptions: true,
                  additionalSearch: (
                    <>
                      {filterControls}
                      <Label className='sr-only' htmlFor={`${id}-type`}>
                        {t('Library type')}
                      </Label>
                      <NativeSelect
                        id={`${id}-type`}
                        value={groupType}
                        onChange={(event) => {
                          setGroupType(event.target.value as AssetGroupType)
                          setGroupId('')
                          setAdding(null)
                          setGroupDialog(null)
                          setValidating(false)
                          setDeleting(null)
                        }}
                      >
                        <option value='AIGC'>{t('Virtual people')}</option>
                        <option value='LivenessFace'>{t('Real people')}</option>
                      </NativeSelect>
                      <div className='flex min-w-0 items-center gap-2'>
                        <Label className='sr-only' htmlFor={id}>
                          {t('Asset group')}
                        </Label>
                        <NativeSelect
                          id={id}
                          className='w-48 max-w-full'
                          value={group?.Id ?? ''}
                          onChange={(event) => setGroupId(event.target.value)}
                          disabled={groups.isPending}
                        >
                          {!items.length && (
                            <option value=''>{t('No asset groups')}</option>
                          )}
                          {items.map((item) => (
                            <option value={item.Id} key={item.Id}>
                              {scope.allUsers
                                ? `${item.Name} (${item.UserId})`
                                : item.Name}
                            </option>
                          ))}
                        </NativeSelect>
                        <DropdownMenu>
                          <DropdownMenuTrigger
                            render={
                              <Button
                                variant='ghost'
                                size='icon'
                                aria-label={t('Group actions')}
                              />
                            }
                          >
                            <MoreHorizontal className='size-4' />
                          </DropdownMenuTrigger>
                          <DropdownMenuContent className='w-60'>
                            <DropdownMenuGroup>
                              {group && (
                                <DropdownMenuLabel className='break-all'>
                                  {group.Id}
                                </DropdownMenuLabel>
                              )}
                              <DropdownMenuItem
                                disabled={!group}
                                onClick={() =>
                                  group && void copyToClipboard(group.Id)
                                }
                              >
                                <Copy className='size-4' />
                                {t('Copy group ID')}
                              </DropdownMenuItem>
                              <DropdownMenuItem
                                disabled={scope.allUsers}
                                onClick={() =>
                                  groupType === 'LivenessFace'
                                    ? setValidating(true)
                                    : setGroupDialog({
                                        project,
                                        scope: { ...scope },
                                      })
                                }
                              >
                                {groupType === 'LivenessFace'
                                  ? t('Verify a person')
                                  : t('Create asset group')}
                              </DropdownMenuItem>
                              <DropdownMenuItem
                                disabled={!group || scope.allUsers || frozen}
                                onClick={() =>
                                  group &&
                                  setGroupDialog({
                                    group,
                                    project,
                                    scope: { ...scope },
                                  })
                                }
                              >
                                {t('Rename')}
                              </DropdownMenuItem>
                            </DropdownMenuGroup>
                            <DropdownMenuSeparator />
                            <DropdownMenuGroup>
                              <DropdownMenuItem
                                variant='destructive'
                                disabled={!group || scope.allUsers || frozen}
                                onClick={() => {
                                  remove.reset()
                                  if (group) {
                                    setDeleting({ group, scope: { ...scope } })
                                  }
                                }}
                              >
                                {t('Delete group')}
                              </DropdownMenuItem>
                            </DropdownMenuGroup>
                          </DropdownMenuContent>
                        </DropdownMenu>
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
                      {scope.allUsers && (
                        <span className='text-muted-foreground text-xs'>
                          {t('Select a user ID to create or change assets.')}
                        </span>
                      )}
                    </>
                  ),
                  preActions: (
                    <>
                      {filterActions}
                      <Button
                        variant='ghost'
                        size='icon'
                        aria-label={t('Refresh')}
                        disabled={groups.isFetching}
                        onClick={() =>
                          void client.invalidateQueries({
                            queryKey: assetQueryKeys.all,
                          })
                        }
                      >
                        <RefreshCw className='size-4' />
                      </Button>
                    </>
                  ),
                }}
                emptyState={emptyState}
              />
            </div>
          </div>
        </SectionPageLayout.Content>
      </SectionPageLayout>
      {groupDialog && !frozen && (
        <AssetGroupDialog
          group={groupDialog.group}
          project={groupDialog.project}
          scope={groupDialog.scope}
          onClose={() => setGroupDialog(null)}
        />
      )}
      {validating && !scope.allUsers && (
        <Suspense fallback={<LoadingState />}>
          <AssetValidationDialog
            project={project}
            scope={scope}
            onClose={() => setValidating(false)}
            onCreated={(createdId) => {
              setGroupId(createdId)
              setValidating(false)
            }}
          />
        </Suspense>
      )}
      {adding && canAddToGroup && (
        <AssetCreateDialog
          group={adding.group}
          scope={adding.scope}
          onClose={() => setAdding(null)}
        />
      )}
      <ConfirmDialog
        open={Boolean(deleting)}
        onOpenChange={(open) => !open && !remove.isPending && setDeleting(null)}
        title={t('Delete asset group')}
        desc={t(
          'Delete {{name}} and all assets in this group? Their asset URIs will no longer work.',
          { name: deleting?.group.Name }
        )}
        destructive
        confirmText={t('Delete')}
        isLoading={remove.isPending}
        handleConfirm={() => deleting && remove.mutate(deleting)}
      >
        {deleting?.group.GroupType === 'LivenessFace' && (
          <p className='text-muted-foreground text-sm'>
            {t(
              'Uploads and new uses are paused while the provider confirms deletion. Originals are kept until deletion is confirmed.'
            )}
          </p>
        )}
        {remove.isError && (
          <p role='alert' className='text-destructive text-sm'>
            {getServerErrorMessage(remove.error)}
          </p>
        )}
      </ConfirmDialog>
    </>
  )
}
