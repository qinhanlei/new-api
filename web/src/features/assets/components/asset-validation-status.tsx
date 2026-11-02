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
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Badge } from '@/components/ui/badge'

import type { AssetValidationSummary } from '../types'

export function AssetValidationStatus(props: {
  status: AssetValidationSummary['Status']
  needsReview?: boolean
  bindingPending?: boolean
  throttleUntil?: number
}) {
  const { t } = useTranslation()
  const [now, setNow] = useState(Date.now)
  const throttleUntil =
    props.status !== 'created' &&
    props.status !== 'expired' &&
    !props.bindingPending &&
    !props.needsReview
      ? props.throttleUntil || 0
      : 0
  useEffect(() => {
    const remaining = throttleUntil - Date.now()
    if (remaining <= 0) return
    const timer = window.setTimeout(
      () => setNow(Date.now()),
      Math.min(remaining, 2_147_483_647)
    )
    return () => window.clearTimeout(timer)
  }, [throttleUntil, now])
  let label = t('Awaiting verification')
  switch (props.status) {
    case 'creating':
      label = t('Creating verification')
      break
    case 'verifying':
      label = t('Checking verification')
      break
    case 'created':
      label = t('Verified')
      break
    case 'failed':
      label = t('Verification could not start')
      break
    case 'expired':
      label = t('Verification link expired')
      break
    case 'unknown':
      label = t('Verification outcome unknown')
      break
  }
  if (props.status !== 'created') {
    if (props.bindingPending) label = t('Linking person group')
    else if (props.needsReview) label = t('Result needs review')
    else if (throttleUntil > Math.max(now, Date.now())) {
      label = t('Waiting to retry')
    }
  }
  return <Badge variant='secondary'>{label}</Badge>
}
