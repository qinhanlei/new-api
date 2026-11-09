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
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { ConfirmDialog } from '@/components/confirm-dialog'
import {
  sideDrawerContentClassName,
  sideDrawerFormClassName,
  sideDrawerHeaderClassName,
} from '@/components/drawer-layout'
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'
import { useCanEditModelPricing } from '@/features/model-pricing/api'

import { BillingRatesEditor } from './billing-rates-editor'

export function BillingRatesDrawer(props: {
  open: boolean
  onOpenChange: (open: boolean) => void
  userId?: number
  username?: string
}) {
  const { t } = useTranslation()
  const canEdit = useCanEditModelPricing()
  const [dirty, setDirty] = useState(false)
  const [saving, setSaving] = useState(false)
  const [discardOpen, setDiscardOpen] = useState(false)
  if (!canEdit) return null
  return (
    <>
      <Sheet
        open={props.open}
        onOpenChange={(open) => {
          if (saving) return
          if (!open && dirty) setDiscardOpen(true)
          else props.onOpenChange(open)
        }}
      >
        <SheetContent className={sideDrawerContentClassName('sm:max-w-4xl')}>
          <SheetHeader className={sideDrawerHeaderClassName()}>
            <SheetTitle>
              {t(props.userId ? 'User model rates' : 'Global model rates')}
            </SheetTitle>
            <SheetDescription>
              {props.userId
                ? `${props.username ?? props.userId} · ${t('Model-specific billing rates')}`
                : t('Model-specific billing rates')}
            </SheetDescription>
          </SheetHeader>
          <div className={sideDrawerFormClassName()}>
            {props.open && (
              <BillingRatesEditor
                key={props.userId ?? 0}
                userId={props.userId}
                onDirtyChange={setDirty}
                onPendingChange={setSaving}
              />
            )}
          </div>
        </SheetContent>
      </Sheet>
      <ConfirmDialog
        disabled={saving}
        open={discardOpen}
        onOpenChange={setDiscardOpen}
        title={t('Discard changes')}
        desc={t('Your unsaved changes will be lost.')}
        confirmText={t('Discard changes')}
        handleConfirm={() => {
          setDiscardOpen(false)
          setDirty(false)
          props.onOpenChange(false)
        }}
      />
    </>
  )
}
