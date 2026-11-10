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
import { Field, FieldLabel } from '@/components/ui/field'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { billingExpressionCurrency } from '@/lib/billing-currency'
import type { BillingCurrency } from '@/lib/currency'

export function BillingCurrencySelector(props: {
  expression: string
  defaultExpression: string
  onChange: (expression: string) => void
}) {
  const { t } = useTranslation()
  const [pending, setPending] = useState<BillingCurrency | null>(null)
  const currency = billingExpressionCurrency(props.expression)
  const items = [
    { value: 'USD', label: t('US dollar (USD)') },
    { value: 'CNY', label: t('Chinese yuan (CNY)') },
  ]
  return (
    <>
      <Field className='gap-2'>
        <FieldLabel>{t('Billing currency')}</FieldLabel>
        <Select
          items={items}
          value={currency}
          onValueChange={(value) => {
            if ((value === 'USD' || value === 'CNY') && value !== currency) {
              setPending(value)
            }
          }}
        >
          <SelectTrigger
            className='w-full sm:w-56'
            aria-label={t('Billing currency')}
          >
            <SelectValue />
          </SelectTrigger>
          <SelectContent alignItemWithTrigger={false}>
            {items.map((item) => (
              <SelectItem key={item.value} value={item.value}>
                {item.label}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <p className='text-muted-foreground text-xs'>
          {t(
            'Prices, wallet deductions and usage statistics use this currency. CNY billing requires a CNY wallet and an enabled CNY token budget.'
          )}
        </p>
      </Field>
      <ConfirmDialog
        open={pending !== null}
        onOpenChange={(open) => {
          if (!open) setPending(null)
        }}
        title={t('Change billing currency')}
        desc={t(
          'Changing the billing currency clears the current prices. Enter new prices in the selected currency before saving. No currency conversion is performed.'
        )}
        confirmText={t('Clear prices and continue')}
        destructive
        handleConfirm={() => {
          if (!pending) return
          props.onChange(`v2:${pending}:${props.defaultExpression}`)
          setPending(null)
        }}
      />
    </>
  )
}
