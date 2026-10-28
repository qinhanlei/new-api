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
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import { getServerErrorMessage } from '@/lib/server-error-message'

import { assetQueryKeys, executeAssetAction } from '../api'
import type { AssetGroup, AssetScope } from '../types'

export function AssetGroupLifecycle(props: {
  group: AssetGroup
  scope: AssetScope
  onRetryDelete: () => void
}) {
  const { t } = useTranslation()
  const client = useQueryClient()
  const cancel = useMutation({
    mutationFn: () =>
      executeAssetAction(
        'CancelAssetGroupDeletion',
        {
          ProjectName: props.group.ProjectName,
          Id: props.group.Id,
        },
        props.scope
      ),
    onSuccess: () => {
      void client.invalidateQueries({ queryKey: assetQueryKeys.all })
    },
    meta: { errorToast: false },
  })
  let status = t('Deletion pending')
  if (props.group.Status === 'delete_unknown') {
    status = t('Deletion outcome unknown')
  }
  if (props.group.Status === 'delete_blocked') status = t('Deletion blocked')
  if (!props.group.Status || props.group.Status === 'active') {
    return (
      <p className='text-muted-foreground shrink-0 text-sm'>
        {t(
          'Each group belongs to one person and stays on its verification channel.'
        )}
      </p>
    )
  }
  return (
    <div
      role='status'
      className='shrink-0 space-y-2 rounded-lg border p-3 text-sm'
    >
      <p className='font-medium'>{status}</p>
      <p className='text-muted-foreground'>
        {t(
          'Uploads and new uses are paused while the provider confirms deletion. Originals are kept until deletion is confirmed.'
        )}
      </p>
      {props.group.Error && (
        <p className='text-destructive break-words'>
          {props.group.Error.Code}: {t(props.group.Error.Message)}
        </p>
      )}
      {!props.scope.allUsers &&
        (props.group.Status === 'delete_blocked' ||
          props.group.Status === 'delete_unknown') && (
          <div className='flex flex-wrap gap-2'>
            <Button
              variant='outline'
              size='sm'
              onClick={props.onRetryDelete}
              disabled={cancel.isPending}
            >
              {t('Retry deletion')}
            </Button>
            {props.group.Status === 'delete_blocked' && (
              <Button
                variant='outline'
                size='sm'
                onClick={() => cancel.mutate()}
                disabled={cancel.isPending}
              >
                {t('Cancel deletion')}
              </Button>
            )}
          </div>
        )}
      {cancel.isError && (
        <p role='alert' className='text-destructive break-words'>
          {getServerErrorMessage(cancel.error)}
        </p>
      )}
    </div>
  )
}
