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
import { useTranslation } from 'react-i18next'

import { CopyButton } from '@/components/copy-button'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from '@/components/ui/collapsible'
import {
  ADMIN_PERMISSION_ACTIONS,
  ADMIN_PERMISSION_RESOURCES,
  hasPermission,
} from '@/lib/admin-permissions'
import { ROLE } from '@/lib/roles'
import { translateServerText } from '@/lib/server-error-message'
import { formatDateTimeObject } from '@/lib/time'
import { useAuthStore } from '@/stores/auth-store'

import type { AssetValidationSummary } from '../types'
import { AssetValidationStatus } from './asset-validation-status'

export function AssetValidationRecordTime(props: { value: number }) {
  const { t } = useTranslation()
  const date = new Date(props.value * 1000)
  if (!props.value || !Number.isFinite(date.getTime())) return t('Not recorded')
  return <time dateTime={date.toISOString()}>{formatDateTimeObject(date)}</time>
}

export function AssetValidationRecordActions(props: {
  record: AssetValidationSummary
  disabled?: boolean
  onContinue: (record: AssetValidationSummary) => void
  onViewGroup: (id: string) => void
}) {
  const { t } = useTranslation()
  if (props.record.GroupId) {
    return (
      <Button
        size='sm'
        variant='outline'
        disabled={props.disabled}
        onClick={() =>
          props.record.GroupId && props.onViewGroup(props.record.GroupId)
        }
      >
        {t('View group')}
      </Button>
    )
  }
  if (!props.record.CanContinue) return null
  return (
    <Button
      size='sm'
      variant='outline'
      disabled={props.disabled}
      onClick={() => props.onContinue(props.record)}
    >
      {props.record.BindingPending
        ? t('Continue binding')
        : t('Continue verification')}
    </Button>
  )
}

export function AssetValidationRecordDetail(props: {
  record: AssetValidationSummary
  project: string
  disabled: boolean
  onContinue: (record: AssetValidationSummary) => void
  onViewGroup: (id: string) => void
  onArchive: (record: AssetValidationSummary) => void
  onRestore: (record: AssetValidationSummary) => void
  onStartNew: () => void
}) {
  const { t } = useTranslation()
  const record = props.record
  const currentUser = useAuthStore((state) => state.auth.user)
  const canReadChannels =
    (currentUser?.role ?? 0) >= ROLE.ADMIN &&
    hasPermission(
      currentUser,
      ADMIN_PERMISSION_RESOURCES.CHANNEL,
      ADMIN_PERMISSION_ACTIONS.READ
    )
  const completed = Boolean(record.GroupId)
  const ended =
    !completed &&
    !record.CanPoll &&
    !record.BindingPending &&
    !record.NeedsReview &&
    ['failed', 'expired'].includes(record.Status)
  let guidance = t('Open this verification to continue checking its result.')
  if (completed) {
    guidance = t('Verification completed. View the associated person group.')
  } else if (record.BindingPending) {
    guidance = t(
      'The verification result is saved. Continue to link the person group.'
    )
  } else if (record.NeedsReview) {
    guidance =
      record.ExpiresAt <= Math.floor(Date.now() / 1000)
        ? t(
            'The link has expired. The verification result still needs to be checked with the provider.'
          )
        : t(
            'The verification outcome is unknown. Check the record with the provider before starting again.'
          )
  } else if (ended) {
    guidance = t('This verification has ended without a person group.')
  }
  if (!completed && record.ContinueError) guidance = ''
  return (
    <div className='space-y-5'>
      <div className='space-y-2'>
        <h3 className='text-lg font-semibold break-words'>{record.Name}</h3>
        <div className='flex flex-wrap gap-2'>
          <AssetValidationStatus
            status={completed ? 'created' : record.Status}
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
        {guidance && (
          <p className='text-muted-foreground text-sm break-words'>
            {guidance}
          </p>
        )}
        {!completed && record.ContinueError && (
          <p role='status' className='text-sm break-words'>
            {t(record.ContinueError.Message)}
          </p>
        )}
      </div>
      <dl className='grid grid-cols-1 gap-4 text-sm sm:grid-cols-2'>
        <div className='min-w-0'>
          <dt className='text-muted-foreground'>{t('Project')}</dt>
          <dd className='break-words'>{record.ProjectName || props.project}</dd>
        </div>
        {canReadChannels && Boolean(record.ChannelName || record.ChannelId) && (
          <div className='min-w-0'>
            <dt className='text-muted-foreground'>{t('Channel')}</dt>
            <dd className='break-words'>
              {record.ChannelName ||
                t('Channel {{id}}', { id: record.ChannelId })}
            </dd>
          </div>
        )}
        <div>
          <dt className='text-muted-foreground'>{t('Created At')}</dt>
          <dd>
            <AssetValidationRecordTime value={record.CreatedAt} />
          </dd>
        </div>
        <div>
          <dt className='text-muted-foreground'>{t('Last checked')}</dt>
          <dd>
            <AssetValidationRecordTime value={record.LastCheckedAt} />
          </dd>
        </div>
        <div>
          <dt className='text-muted-foreground'>
            {t('Verification link expires')}
          </dt>
          <dd>
            <AssetValidationRecordTime value={record.ExpiresAt} />
          </dd>
        </div>
        {record.ArchivedAt > 0 && (
          <div>
            <dt className='text-muted-foreground'>{t('Archived at')}</dt>
            <dd>
              <AssetValidationRecordTime value={record.ArchivedAt} />
            </dd>
          </div>
        )}
        <div className='min-w-0 sm:col-span-2'>
          <dt className='text-muted-foreground'>
            {t('Verification record ID')}
          </dt>
          <dd className='flex items-center gap-2'>
            <code className='min-w-0 break-all'>{record.SessionId}</code>
            <CopyButton
              value={record.SessionId}
              tooltip={t('Copy record ID')}
            />
          </dd>
        </div>
        {record.GroupId && (
          <div className='min-w-0 sm:col-span-2'>
            <dt className='text-muted-foreground'>{t('Group ID')}</dt>
            <dd className='flex items-center gap-2'>
              <code className='min-w-0 break-all'>{record.GroupId}</code>
              <CopyButton value={record.GroupId} tooltip={t('Copy group ID')} />
            </dd>
          </div>
        )}
      </dl>
      {record.Error && !canReadChannels && (
        <p role='status' className='text-sm break-words'>
          {translateServerText(t, record.Error.Message)}
        </p>
      )}
      {record.Error && canReadChannels && (
        <Collapsible>
          <CollapsibleTrigger render={<Button variant='ghost' size='sm' />}>
            {t('Diagnostics')}
          </CollapsibleTrigger>
          <CollapsibleContent className='space-y-2 pt-2 text-sm'>
            <p className='font-mono break-all'>{record.Error.Code}</p>
            <p className='break-words'>
              {translateServerText(t, record.Error.Message)}
            </p>
          </CollapsibleContent>
        </Collapsible>
      )}
      <div className='flex flex-wrap gap-2'>
        <AssetValidationRecordActions
          record={record}
          disabled={props.disabled}
          onContinue={props.onContinue}
          onViewGroup={props.onViewGroup}
        />
        {ended && record.CanArchive && (
          <Button
            size='sm'
            variant='outline'
            disabled={props.disabled}
            onClick={props.onStartNew}
          >
            {t('Start a new verification')}
          </Button>
        )}
        {record.CanArchive &&
          (record.ArchivedAt > 0 ? (
            <Button
              size='sm'
              variant='outline'
              disabled={props.disabled}
              onClick={() => props.onRestore(record)}
            >
              {t('Restore')}
            </Button>
          ) : (
            <Button
              size='sm'
              variant='outline'
              disabled={props.disabled}
              onClick={() => props.onArchive(record)}
            >
              {t('Archive')}
            </Button>
          ))}
      </div>
    </div>
  )
}
