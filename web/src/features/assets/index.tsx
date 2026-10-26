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
import { ROLE } from '@/lib/roles'
import { getServerErrorMessage } from '@/lib/server-error-message'
import { useAuthStore } from '@/stores/auth-store'

import { assetQueryKeys, getAssetConfig } from './api'
import { AssetBrowser } from './components/asset-browser'
import type { AssetScope } from './types'

export function Assets() {
  const { t } = useTranslation()
  const id = useId()
  const user = useAuthStore((state) => state.auth.user)
  const admin = (user?.role ?? 0) >= ROLE.ADMIN
  const [filters, setFilters] = useState<{
    project: string
    scope: AssetScope
  }>({ project: 'default', scope: {} })
  const [scopeMode, setScopeMode] = useState('self')
  const config = useQuery({
    queryKey: [...assetQueryKeys.all, 'config'],
    queryFn: ({ signal }) => getAssetConfig(signal),
    meta: { errorToast: false },
  })
  return (
    <SectionPageLayout>
      <SectionPageLayout.Title>{t('Asset library')}</SectionPageLayout.Title>
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
            description={t('Ask an administrator to configure asset storage.')}
          />
        )}
        {config.data?.enabled && (
          <div className='space-y-4'>
            <form
              className='flex flex-wrap items-end gap-3'
              onSubmit={(event) => {
                event.preventDefault()
                const values = new FormData(event.currentTarget)
                const project =
                  String(values.get('project') ?? '').trim() || 'default'
                let scope: AssetScope = {}
                if (scopeMode === 'all') scope = { allUsers: true }
                if (scopeMode === 'user') {
                  scope = { userId: Number(values.get('userId')) }
                }
                setFilters({ project, scope })
              }}
            >
              <div className='space-y-1.5'>
                <Label htmlFor={`${id}-project`}>{t('Project name')}</Label>
                <Input
                  id={`${id}-project`}
                  name='project'
                  defaultValue='default'
                  required
                  maxLength={64}
                  className='w-48'
                />
              </div>
              {admin && (
                <>
                  <div className='space-y-1.5'>
                    <Label htmlFor={`${id}-scope`}>{t('Owner')}</Label>
                    <NativeSelect
                      id={`${id}-scope`}
                      value={scopeMode}
                      onChange={(event) => setScopeMode(event.target.value)}
                    >
                      <option value='self'>{t('My assets')}</option>
                      <option value='user'>{t('User ID')}</option>
                      <option value='all'>{t('All users')}</option>
                    </NativeSelect>
                  </div>
                  {scopeMode === 'user' && (
                    <div className='space-y-1.5'>
                      <Label htmlFor={`${id}-user`}>{t('User ID')}</Label>
                      <Input
                        id={`${id}-user`}
                        name='userId'
                        type='number'
                        min={1}
                        step={1}
                        required
                        className='w-32'
                      />
                    </div>
                  )}
                </>
              )}
              <Button type='submit' variant='outline'>
                {t('Apply')}
              </Button>
            </form>
            <AssetBrowser
              key={`${filters.project}:${filters.scope.userId ?? 'self'}:${filters.scope.allUsers ?? false}`}
              project={filters.project}
              scope={filters.scope}
              admin={admin}
            />
          </div>
        )}
      </SectionPageLayout.Content>
    </SectionPageLayout>
  )
}
