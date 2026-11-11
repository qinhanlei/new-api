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
import type { UseQueryResult } from '@tanstack/react-query'
import type {
  ColumnDef,
  OnChangeFn,
  RowSelectionState,
} from '@tanstack/react-table'
import { RefreshCw } from 'lucide-react'
import { useId, useMemo, type Dispatch, type SetStateAction } from 'react'
import { useTranslation } from 'react-i18next'

import {
  DataTableBulkActions,
  DataTablePage,
  DataTableToolbar,
  useDataTable,
} from '@/components/data-table'
import { ErrorState } from '@/components/error-state'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Label } from '@/components/ui/label'
import { NativeSelect } from '@/components/ui/native-select'
import { getServerErrorMessage } from '@/lib/server-error-message'

import type {
  AssetValidationRecordFilter,
  AssetValidationRecords,
  AssetValidationSummary,
} from '../types'
import {
  AssetValidationRecordActions,
  AssetValidationRecordTime,
} from './asset-validation-record-detail'
import { AssetValidationStatus } from './asset-validation-status'

export function AssetValidationRecordList(props: {
  query: UseQueryResult<AssetValidationRecords>
  filter: AssetValidationRecordFilter
  onFilterChange: Dispatch<SetStateAction<AssetValidationRecordFilter>>
  selection: RowSelectionState
  onSelectionChange: OnChangeFn<RowSelectionState>
  disabled: boolean
  onDetails: (
    record: AssetValidationSummary,
    trigger: HTMLButtonElement
  ) => void
  onContinue: (record: AssetValidationSummary) => void
  onViewGroup: (id: string) => void
  onArchive: (records: AssetValidationSummary[]) => void
  onRestore: (records: AssetValidationSummary[]) => void
  onStartNew: () => void
}) {
  const { t } = useTranslation()
  const id = useId()
  const disabled = props.disabled
  const onContinue = props.onContinue
  const onViewGroup = props.onViewGroup
  const onDetails = props.onDetails
  const columns = useMemo<ColumnDef<AssetValidationSummary>[]>(
    () => [
      {
        id: 'select',
        header: ({ table }) => (
          <Checkbox
            checked={table.getIsAllPageRowsSelected()}
            indeterminate={table.getIsSomePageRowsSelected()}
            disabled={
              disabled ||
              !table.getRowModel().rows.some((row) => row.getCanSelect())
            }
            onCheckedChange={(checked) =>
              table.toggleAllPageRowsSelected(Boolean(checked))
            }
            aria-label={t('Select all')}
          />
        ),
        cell: ({ row }) => (
          <Checkbox
            checked={row.getIsSelected()}
            disabled={disabled || !row.getCanSelect()}
            onCheckedChange={(checked) => row.toggleSelected(Boolean(checked))}
            aria-label={t('Select {{name}}', { name: row.original.Name })}
          />
        ),
        size: 40,
        minSize: 40,
        maxSize: 40,
        enableHiding: false,
      },
      {
        id: 'name',
        accessorKey: 'Name',
        header: t('Name'),
        size: 220,
        minSize: 160,
        cell: ({ row: { original: record } }) => (
          <div className='min-w-0 space-y-1 whitespace-normal'>
            <p className='break-words'>{record.Name}</p>
            <p className='text-muted-foreground text-xs'>
              <AssetValidationRecordTime value={record.CreatedAt} />
            </p>
          </div>
        ),
      },
      {
        id: 'status',
        header: t('Status'),
        size: 160,
        minSize: 140,
        cell: ({ row: { original: record } }) => (
          <div className='flex flex-wrap gap-1'>
            <AssetValidationStatus
              status={record.GroupId ? 'created' : record.Status}
              needsReview={record.NeedsReview}
              bindingPending={record.BindingPending}
              throttleUntil={
                record.Error?.Code === 'Throttling'
                  ? (record.NextPollAt || 0) * 1000
                  : 0
              }
            />
            {record.ArchivedAt > 0 && (
              <Badge variant='outline'>{t('Archived')}</Badge>
            )}
          </div>
        ),
      },
      {
        id: 'actions',
        header: t('Actions'),
        size: 220,
        minSize: 180,
        cell: ({ row: { original: record } }) => (
          <div className='flex flex-wrap items-center gap-1'>
            <AssetValidationRecordActions
              record={record}
              disabled={disabled}
              onContinue={onContinue}
              onViewGroup={onViewGroup}
            />
            <Button
              size='sm'
              variant='ghost'
              aria-label={t('View details: {{name}}', { name: record.Name })}
              disabled={disabled}
              onClick={(event) => {
                onDetails(record, event.currentTarget)
              }}
            >
              {t('View details')}
            </Button>
          </div>
        ),
      },
    ],
    [disabled, t, onViewGroup, onContinue, onDetails]
  )
  const { table } = useDataTable({
    data: props.query.data?.Items ?? [],
    columns,
    columnFilters: [],
    totalCount: props.query.data?.TotalCount ?? 0,
    globalFilter: props.filter.name,
    onGlobalFilterChange: (updater) =>
      props.onFilterChange((previous) => ({
        ...previous,
        page: 1,
        name:
          typeof updater === 'function'
            ? updater(previous.name ?? '')
            : updater,
      })),
    pagination: {
      pageIndex: props.filter.page - 1,
      pageSize: props.filter.page_size,
    },
    onPaginationChange: (updater) =>
      props.onFilterChange((previous) => {
        const next =
          typeof updater === 'function'
            ? updater({
                pageIndex: previous.page - 1,
                pageSize: previous.page_size,
              })
            : updater
        return {
          ...previous,
          page: previous.page_size === next.pageSize ? next.pageIndex + 1 : 1,
          page_size: next.pageSize,
        }
      }),
    ensurePageInRange: (pages) => {
      if (!props.query.data) return
      props.onFilterChange((previous) =>
        previous.page > Math.max(1, pages)
          ? { ...previous, page: Math.max(1, pages) }
          : previous
      )
    },
    manualFiltering: true,
    manualPagination: true,
    manualSorting: true,
    enableSorting: false,
    enableRowSelection: (row) =>
      !props.disabled &&
      row.original.CanArchive &&
      (!row.original.NeedsReview || row.original.ArchivedAt > 0),
    rowSelection: props.selection,
    onRowSelectionChange: props.onSelectionChange,
    getRowId: (record) => record.SessionId,
  })
  const selected = table.getSelectedRowModel().rows.map((row) => row.original)
  const toolbarProps = {
    searchPlaceholder: t('Filter by name...'),
    searchDebounceMs: 300,
    hideViewOptions: true,
    additionalSearch: (
      <>
        <Label htmlFor={`${id}-status`} className='sr-only'>
          {t('Verification status')}
        </Label>
        <NativeSelect
          id={`${id}-status`}
          value={props.filter.status}
          onChange={(event) =>
            props.onFilterChange({
              ...props.filter,
              page: 1,
              status: event.target
                .value as AssetValidationRecordFilter['status'],
            })
          }
        >
          <option value='all'>{t('All statuses')}</option>
          <option value='pending'>{t('Pending')}</option>
          <option value='completed'>{t('Verified')}</option>
          <option value='needs_review'>{t('Result needs review')}</option>
          <option value='ended'>{t('Ended')}</option>
        </NativeSelect>
        <Label htmlFor={`${id}-archive`} className='sr-only'>
          {t('Archive status')}
        </Label>
        <NativeSelect
          id={`${id}-archive`}
          value={props.filter.archived}
          onChange={(event) =>
            props.onFilterChange({
              ...props.filter,
              page: 1,
              archived: event.target
                .value as AssetValidationRecordFilter['archived'],
            })
          }
        >
          <option value='active'>{t('Active records')}</option>
          <option value='archived'>{t('Archived')}</option>
          <option value='all'>{t('All records')}</option>
        </NativeSelect>
      </>
    ),
    preActions: (
      <Button
        size='icon'
        variant='ghost'
        aria-label={t('Refresh records')}
        disabled={props.query.isFetching || props.disabled}
        onClick={() => void props.query.refetch()}
      >
        <RefreshCw className='size-4' aria-hidden='true' />
      </Button>
    ),
  }
  return props.query.isError && !props.query.data ? (
    <>
      <DataTableToolbar table={table} {...toolbarProps} />
      <ErrorState
        description={getServerErrorMessage(props.query.error)}
        onRetry={() => void props.query.refetch()}
      />
    </>
  ) : (
    <DataTablePage
      table={table}
      columns={columns}
      isLoading={props.query.isPending}
      isFetching={props.query.isFetching}
      toolbarProps={toolbarProps}
      paginationInFooter={false}
      compactPagination
      emptyTitle={t('No verification records')}
      mobileProps={{ enableRowSelection: true }}
      emptyAction={
        <Button onClick={props.onStartNew}>{t('Start verification')}</Button>
      }
      afterTable={
        <>
          <DataTableBulkActions
            table={table}
            entityName={t('Verification record')}
            placement='inline'
          >
            <Button
              variant='outline'
              disabled={
                props.disabled ||
                selected.length === 0 ||
                selected.some(
                  (record) => record.ArchivedAt > 0 || record.NeedsReview
                )
              }
              onClick={() => {
                props.onArchive(selected)
              }}
            >
              {t('Archive selected')}
            </Button>
            <Button
              variant='outline'
              disabled={
                props.disabled ||
                selected.length === 0 ||
                selected.some((record) => record.ArchivedAt <= 0)
              }
              onClick={() => props.onRestore(selected)}
            >
              {t('Restore selected')}
            </Button>
          </DataTableBulkActions>
          {props.query.isError && (
            <p role='alert' className='text-destructive text-sm'>
              {getServerErrorMessage(props.query.error)}
            </p>
          )}
        </>
      }
    />
  )
}
