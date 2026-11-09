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

import type { PricingModel } from '@/features/pricing/types'
import { toIntlLocale } from '@/i18n/languages'

import { billingRateSourceKey } from './rates'
import type { BillingRateSnapshot } from './types'

export function BillingRateLabel(props: {
  snapshot?: BillingRateSnapshot
  fallbackRate?: number
}) {
  const { t, i18n } = useTranslation()
  const rate = props.snapshot?.rate ?? props.fallbackRate
  if (rate == null) return null
  const value = new Intl.NumberFormat(
    toIntlLocale(i18n.resolvedLanguage || i18n.language),
    { maximumFractionDigits: 6 }
  ).format(rate)
  return (
    <span className='text-muted-foreground text-xs tabular-nums'>
      {t(
        props.snapshot
          ? billingRateSourceKey(props.snapshot.source)
          : 'Group Ratio'
      )}{' '}
      {value}×
    </span>
  )
}

export function ModelRateLabel(props: {
  model: PricingModel
  selectedGroup?: string
}) {
  const rate = props.model.effective_rates
  if (!rate) return null
  let selected = props.selectedGroup ? rate[props.selectedGroup] : undefined
  if (!selected) {
    for (const group of props.model.enable_groups) {
      if (rate[group] && (!selected || rate[group].rate < selected.rate)) {
        selected = rate[group]
      }
    }
  }
  if (
    !selected ||
    (selected.source !== 'user' && selected.source !== 'model')
  ) {
    return null
  }
  return <BillingRateLabel snapshot={selected} />
}
