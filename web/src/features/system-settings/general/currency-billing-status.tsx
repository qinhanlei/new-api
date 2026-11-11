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

import { formatTimestampToDate } from '@/lib/format'
import { translateServerText } from '@/lib/server-error-message'

import type { CurrencyBillingSettings } from './currency-billing-api'

export function CurrencyBillingStatus(props: {
  settings: CurrencyBillingSettings
}) {
  const { t } = useTranslation()
  const settings = props.settings
  const rate = settings.rate
  const observation = settings.latest_observation
  let statusText = t('Exchange rate ready')
  if (settings.status === 'last_good') {
    statusText = t('Using last accepted rate')
  }
  if (settings.status === 'unavailable') {
    statusText = t('Cross-currency payment unavailable')
  }
  const changeMatch =
    /^Exchange rate change exceeds ([0-9]+(?:\.[0-9]+)?)%$/.exec(
      observation.reason
    )
  const reason = changeMatch
    ? t('Exchange rate change exceeds {{percent}}%', {
        percent: changeMatch[1],
      })
    : translateServerText(t, observation.reason)

  return (
    <div
      className='bg-muted/30 space-y-2 rounded-lg border p-3 text-sm'
      role='status'
      aria-label={t('Exchange rate status')}
    >
      <p className='font-medium'>{statusText}</p>
      {rate.rate && settings.status !== 'unavailable' && (
        <p>
          {t('Effective exchange rate')}: 1 USD = {rate.rate} CNY
        </p>
      )}
      {rate.source && (
        <p className='text-muted-foreground text-xs'>
          {rate.source === 'manual' ? t('Manual') : 'Frankfurter'}
          {!!rate.source_time &&
            ` · ${formatTimestampToDate(rate.source_time)}`}
        </p>
      )}
      {settings.policy.dynamic_enabled && observation.observed_at > 0 && (
        <div className='text-muted-foreground space-y-1 text-xs'>
          <p>
            {t('Last fetched')}:{' '}
            {formatTimestampToDate(observation.observed_at)}
          </p>
          {observation.source_time > 0 && (
            <p>
              {t('Source date')}:{' '}
              {formatTimestampToDate(observation.source_time)}
            </p>
          )}
          {observation.rate && (
            <p>
              {t('Latest fetched rate')}: 1 USD = {observation.rate} CNY
            </p>
          )}
          {!observation.accepted && reason && (
            <p className='text-destructive'>{reason}</p>
          )}
        </div>
      )}
    </div>
  )
}
