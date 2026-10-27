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
import { Link } from '@tanstack/react-router'
import { useId, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { EmptyState } from '@/components/empty-state'
import { ErrorState } from '@/components/error-state'
import { SectionPageLayout } from '@/components/layout'
import { LoadingState } from '@/components/loading-state'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { NativeSelect } from '@/components/ui/native-select'
import {
  ADMIN_PERMISSION_ACTIONS,
  ADMIN_PERMISSION_RESOURCES,
  hasPermission,
} from '@/lib/admin-permissions'
import { ROLE } from '@/lib/roles'
import { getServerErrorMessage } from '@/lib/server-error-message'
import { useAuthStore } from '@/stores/auth-store'

import { assetQueryKeys, getAssetConfig } from './api'
import { AssetBrowser } from './components/asset-browser'
import { AssetChannelSettingsDialog } from './components/asset-channel-settings-dialog'
import type { AssetScope } from './types'

export function Assets() {
  const { t } = useTranslation()
  const id = useId()
  const user = useAuthStore((state) => state.auth.user)
  const admin = (user?.role ?? 0) >= ROLE.ADMIN
  const canConfigureChannels =
    admin &&
    hasPermission(
      user,
      ADMIN_PERMISSION_RESOURCES.CHANNEL,
      ADMIN_PERMISSION_ACTIONS.READ
    ) &&
    hasPermission(
      user,
      ADMIN_PERMISSION_RESOURCES.CHANNEL,
      ADMIN_PERMISSION_ACTIONS.SENSITIVE_WRITE
    )
  const [channelsOpen, setChannelsOpen] = useState(false)
  const [filters, setFilters] = useState<{
    project: string
    scope: AssetScope
  }>({ project: 'default', scope: {} })
  const [draft, setDraft] = useState({
    project: 'default',
    scope: 'self',
    userId: '',
  })
  const config = useQuery({
    queryKey: assetQueryKeys.config,
    queryFn: ({ signal }) => getAssetConfig(signal),
    meta: { errorToast: false },
  })
  const settingsActions = (
    <>
      {canConfigureChannels && (
        <Button variant='outline' onClick={() => setChannelsOpen(true)}>
          {t('Channel settings')}
        </Button>
      )}
      {user?.role === ROLE.SUPER_ADMIN && (
        <Button
          variant='outline'
          render={
            <Link
              to='/system-settings/operations/$section'
              params={{ section: 'assets' }}
            />
          }
        >
          {t('Storage settings')}
        </Button>
      )}
    </>
  )
  return (
    <>
      {config.data?.enabled ? (
        <AssetBrowser
          key={`${filters.project}:${filters.scope.userId ?? 'self'}:${filters.scope.allUsers ?? false}`}
          project={filters.project}
          scope={filters.scope}
          admin={admin}
          settingsActions={settingsActions}
          filterActions={
            <Button form={`${id}-filters`} type='submit' variant='outline'>
              {t('Apply')}
            </Button>
          }
          filterControls={
            <form
              id={`${id}-filters`}
              className='contents'
              onSubmit={(event) => {
                event.preventDefault()
                const values = new FormData(event.currentTarget)
                const project =
                  String(values.get('project') ?? '').trim() || 'default'
                let scope: AssetScope = {}
                if (draft.scope === 'all') scope = { allUsers: true }
                if (draft.scope === 'user') {
                  scope = { userId: Number(values.get('userId')) }
                }
                setFilters({ project, scope })
              }}
            >
              <div className='flex items-center gap-2'>
                <Label htmlFor={`${id}-project`}>{t('Project name')}</Label>
                <Input
                  id={`${id}-project`}
                  name='project'
                  value={draft.project}
                  onChange={(event) =>
                    setDraft({ ...draft, project: event.target.value })
                  }
                  required
                  maxLength={64}
                  className='w-32'
                />
              </div>
              {admin && (
                <>
                  <Label className='sr-only' htmlFor={`${id}-scope`}>
                    {t('Owner')}
                  </Label>
                  <NativeSelect
                    id={`${id}-scope`}
                    value={draft.scope}
                    onChange={(event) =>
                      setDraft({ ...draft, scope: event.target.value })
                    }
                  >
                    <option value='self'>{t('My assets')}</option>
                    <option value='user'>{t('User ID')}</option>
                    <option value='all'>{t('All users')}</option>
                  </NativeSelect>
                  {draft.scope === 'user' && (
                    <>
                      <Label className='sr-only' htmlFor={`${id}-user`}>
                        {t('User ID')}
                      </Label>
                      <Input
                        id={`${id}-user`}
                        name='userId'
                        type='number'
                        min={1}
                        step={1}
                        required
                        value={draft.userId}
                        onChange={(event) =>
                          setDraft({ ...draft, userId: event.target.value })
                        }
                        placeholder={t('User ID')}
                        className='w-24'
                      />
                    </>
                  )}
                </>
              )}
            </form>
          }
        />
      ) : (
        <SectionPageLayout fixedContent>
          <SectionPageLayout.Title>
            {t('Asset library')}
          </SectionPageLayout.Title>
          <SectionPageLayout.Actions>
            {settingsActions}
          </SectionPageLayout.Actions>
          <SectionPageLayout.Content>
            {config.isPending && <LoadingState />}
            {config.isError && (
              <ErrorState
                description={getServerErrorMessage(config.error)}
                onRetry={() => void config.refetch()}
              />
            )}
            {config.data && !config.data.enabled && (
              <EmptyState
                title={t('Asset library is disabled')}
                description={t(
                  'Ask an administrator to configure asset storage.'
                )}
              />
            )}
          </SectionPageLayout.Content>
        </SectionPageLayout>
      )}
      {channelsOpen && canConfigureChannels && (
        <AssetChannelSettingsDialog onClose={() => setChannelsOpen(false)} />
      )}
    </>
  )
}
