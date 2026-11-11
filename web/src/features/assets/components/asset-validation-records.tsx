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
import type { RowSelectionState } from '@tanstack/react-table'
import { ArrowLeft, X } from 'lucide-react'
import { useCallback, useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { ConfirmDialog } from '@/components/confirm-dialog'
import {
  sideDrawerContentClassName,
  sideDrawerFormClassName,
  sideDrawerHeaderClassName,
} from '@/components/drawer-layout'
import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { Button } from '@/components/ui/button'
import {
  Sheet,
  SheetClose,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'
import { toIntlLocale } from '@/i18n/languages'
import { formatNumber } from '@/lib/format'
import { getServerErrorMessage } from '@/lib/server-error-message'
import { cn } from '@/lib/utils'

import {
  archiveAssetValidationRecords,
  assetQueryKeys,
  getAssetValidationRecord,
  getAssetValidationRecords,
} from '../api'
import type {
  AssetScope,
  AssetValidationRecordFilter,
  AssetValidationSummary,
} from '../types'
import { AssetValidationRecordDetail } from './asset-validation-record-detail'
import { AssetValidationRecordList } from './asset-validation-record-list'

export function AssetValidationRecordsDrawer(props: {
  project: string
  scope: AssetScope
  onClose: () => void
  onContinue: (record: AssetValidationSummary) => void
  onViewGroup: (id: string) => void | Promise<void>
  onStartNew: () => void
}) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const client = useQueryClient()
  const [filter, setFilter] = useState<AssetValidationRecordFilter>({
    page: 1,
    page_size: 20,
    name: '',
    status: 'all',
    archived: 'active',
  })
  const [viewedId, setViewedId] = useState('')
  const detailsTrigger = useRef<HTMLButtonElement | null>(null)
  const backButton = useRef<HTMLButtonElement>(null)
  const listRegion = useRef<HTMLDivElement>(null)
  useEffect(() => {
    if (viewedId) backButton.current?.focus()
    else if (detailsTrigger.current) {
      if (detailsTrigger.current.isConnected) detailsTrigger.current.focus()
      else listRegion.current?.focus()
    }
  }, [viewedId])
  const [archiveTarget, setArchiveTarget] = useState<
    AssetValidationSummary[] | null
  >(null)
  const query = useQuery({
    queryKey: [
      ...assetQueryKeys.all,
      'validation-records',
      props.project,
      props.scope,
      filter,
    ],
    queryFn: ({ signal }) =>
      getAssetValidationRecords(props.project, props.scope, filter, signal),
    refetchInterval: viewedId ? false : 15_000,
    meta: { errorToast: false },
  })
  const detail = useQuery({
    queryKey: [
      ...assetQueryKeys.all,
      'validation-record',
      props.project,
      props.scope,
      viewedId,
    ],
    queryFn: ({ signal }) =>
      getAssetValidationRecord(viewedId, props.project, props.scope, signal),
    enabled: Boolean(viewedId),
    meta: { errorToast: false },
  })
  const viewGroup = useMutation({
    mutationFn: async (groupId: string) => props.onViewGroup(groupId),
    meta: { errorToast: false },
  })
  const archive = useMutation({
    mutationFn: (request: {
      records: AssetValidationSummary[]
      archived: boolean
    }) =>
      archiveAssetValidationRecords(
        props.project,
        props.scope,
        request.records.map((record) => record.SessionId),
        request.archived
      ),
    onSuccess: (_data, request) => {
      setArchiveTarget(null)
      setViewedId('')
      setSelection((previous) => ({ ...previous, rows: {} }))
      void client.invalidateQueries({ queryKey: assetQueryKeys.all })
      toast.success(
        request.archived
          ? t('Verification records archived')
          : t('Verification records restored')
      )
    },
    meta: { errorToast: false },
  })
  const busy = archive.isPending || viewGroup.isPending
  const selectionContext = JSON.stringify([props.project, props.scope, filter])
  const [selection, setSelection] = useState<{
    context: string
    items: AssetValidationSummary[] | undefined
    rows: RowSelectionState
  }>({ context: selectionContext, items: query.data?.Items, rows: {} })
  if (
    selection.context !== selectionContext ||
    selection.items !== query.data?.Items
  ) {
    const rows: RowSelectionState = {}
    if (selection.context === selectionContext) {
      for (const record of query.data?.Items ?? []) {
        if (
          selection.rows[record.SessionId] &&
          record.CanArchive &&
          (!record.NeedsReview || record.ArchivedAt > 0)
        ) {
          rows[record.SessionId] = true
        }
      }
    }
    setSelection({ context: selectionContext, items: query.data?.Items, rows })
  }
  const openGroup = viewGroup.mutate
  const resetViewGroup = viewGroup.reset
  const showDetails = useCallback(
    (record: AssetValidationSummary, trigger: HTMLButtonElement) => {
      detailsTrigger.current = trigger
      resetViewGroup()
      setViewedId(record.SessionId)
    },
    [resetViewGroup]
  )
  return (
    <Sheet
      open
      onOpenChange={(open) => {
        if (!open && !busy) props.onClose()
      }}
    >
      <SheetContent
        className={sideDrawerContentClassName('sm:max-w-3xl')}
        showCloseButton={false}
      >
        <SheetHeader className={sideDrawerHeaderClassName('relative pr-14')}>
          <SheetTitle>{t('Verification records')}</SheetTitle>
          <SheetDescription>
            {query.data
              ? t('{{count}} pending, including archived records', {
                  count: formatNumber(query.data.PendingCount, locale),
                })
              : t('Pending count unavailable')}
          </SheetDescription>
          <SheetClose
            render={
              <Button
                size='icon-sm'
                variant='ghost'
                className='absolute top-3 right-3'
                disabled={busy}
                aria-label={t('Close')}
              />
            }
          >
            <X aria-hidden='true' />
          </SheetClose>
        </SheetHeader>
        {viewGroup.isError && (
          <p
            role='alert'
            className='text-destructive px-4 pt-3 text-sm break-words'
          >
            {getServerErrorMessage(viewGroup.error)}
          </p>
        )}
        {viewGroup.isPending && (
          <p role='status' className='px-4 pt-3 text-sm'>
            {t('Opening group...')}
          </p>
        )}
        {archive.isPending && !archiveTarget && (
          <p role='status' className='px-4 pt-3 text-sm'>
            {t('Restoring records...')}
          </p>
        )}
        {archive.isError && !archiveTarget && (
          <p
            role='alert'
            className='text-destructive px-4 pt-3 text-sm break-words'
          >
            {getServerErrorMessage(archive.error)}
          </p>
        )}
        <div
          ref={listRegion}
          tabIndex={-1}
          className={cn(
            'flex min-h-0 flex-1 flex-col px-4 py-4 sm:px-6',
            viewedId && 'hidden'
          )}
          hidden={Boolean(viewedId)}
          aria-hidden={Boolean(viewedId)}
        >
          <AssetValidationRecordList
            query={query}
            filter={filter}
            onFilterChange={setFilter}
            selection={selection.rows}
            onSelectionChange={(updater) =>
              setSelection((previous) => ({
                ...previous,
                rows:
                  typeof updater === 'function'
                    ? updater(previous.rows)
                    : updater,
              }))
            }
            disabled={busy}
            onContinue={props.onContinue}
            onViewGroup={openGroup}
            onStartNew={props.onStartNew}
            onDetails={showDetails}
            onArchive={(records) => {
              archive.reset()
              setArchiveTarget(records)
            }}
            onRestore={(records) =>
              archive.mutate({ records, archived: false })
            }
          />
        </div>
        {viewedId && (
          <div className={sideDrawerFormClassName('gap-4')}>
            <Button
              ref={backButton}
              disabled={busy}
              size='sm'
              variant='ghost'
              className='self-start'
              onClick={() => {
                setViewedId('')
                viewGroup.reset()
                archive.reset()
              }}
            >
              <ArrowLeft className='size-4' aria-hidden='true' />
              {t('Back to records')}
            </Button>
            {detail.isPending && <LoadingState />}
            {detail.isError && (
              <ErrorState
                description={getServerErrorMessage(detail.error)}
                onRetry={() => void detail.refetch()}
              />
            )}
            {detail.data && !detail.isError && (
              <AssetValidationRecordDetail
                record={detail.data}
                project={props.project}
                disabled={busy}
                onContinue={props.onContinue}
                onViewGroup={openGroup}
                onStartNew={props.onStartNew}
                onArchive={(record) => {
                  archive.reset()
                  setArchiveTarget([record])
                }}
                onRestore={(record) =>
                  archive.mutate({ records: [record], archived: false })
                }
              />
            )}
          </div>
        )}
        <ConfirmDialog
          open={Boolean(archiveTarget)}
          onOpenChange={(open) => {
            if (!open && !archive.isPending) {
              setArchiveTarget(null)
              archive.reset()
            }
          }}
          title={t('Archive verification records')}
          desc={
            archiveTarget?.some((record) => record.NeedsReview)
              ? t(
                  'Archiving only hides this record from the active list. It still needs review and remains included in the pending count.'
                )
              : t(
                  'Archiving hides these records from the active list. Their person groups are kept, and the records can be restored.'
                )
          }
          confirmText={
            archive.isPending ? t('Archiving records...') : t('Archive')
          }
          isLoading={archive.isPending}
          finalFocus={viewedId ? backButton : listRegion}
          handleConfirm={() => {
            if (archiveTarget) {
              archive.mutate({ records: archiveTarget, archived: true })
            }
          }}
        >
          {archive.isError && (
            <p role='alert' className='text-destructive text-sm break-words'>
              {getServerErrorMessage(archive.error)}
            </p>
          )}
        </ConfirmDialog>
      </SheetContent>
    </Sheet>
  )
}
