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
import { Upload } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { SectionPageLayout } from '@/components/layout'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { useStatus } from '@/hooks/use-status'

import { AssetFormDialog } from './components/asset-form-dialog'
import { AssetsTable } from './components/assets-table'

export function Assets() {
  const { t } = useTranslation()
  const { status } = useStatus()
  const enabled = status?.asset_library_enabled === true
  const [uploadOpen, setUploadOpen] = useState(false)
  return (
    <>
      <SectionPageLayout fixedContent>
        <SectionPageLayout.Title>{t('Asset library')}</SectionPageLayout.Title>
        <SectionPageLayout.Actions>
          <Button disabled={!enabled} onClick={() => setUploadOpen(true)}>
            <Upload />
            {t('Upload asset')}
          </Button>
        </SectionPageLayout.Actions>
        <SectionPageLayout.Content>
          <div className='flex h-full min-h-0 flex-col gap-3'>
            {!enabled && (
              <Alert>
                <AlertDescription>
                  {t(
                    'Asset uploads are disabled. Contact your administrator to enable the asset library.'
                  )}
                </AlertDescription>
              </Alert>
            )}
            <AssetsTable />
          </div>
        </SectionPageLayout.Content>
      </SectionPageLayout>
      {uploadOpen && <AssetFormDialog onClose={() => setUploadOpen(false)} />}
    </>
  )
}
