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
import { Activity, BarChart3, WalletCards } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { IconBadge, type IconBadgeTone } from '@/components/ui/icon-badge'
import { Skeleton } from '@/components/ui/skeleton'
import { toIntlLocale } from '@/i18n/languages'
import { formatBillingQuota } from '@/lib/currency'
import { formatNumber } from '@/lib/format'
import { useSystemConfigStore } from '@/stores/system-config-store'

import { getCurrencyWallet } from '../currency-api'
import type { UserWalletData } from '../types'

interface WalletStatsCardProps {
  user: UserWalletData | null
  loading?: boolean
}

export function WalletStatsCard(props: WalletStatsCardProps) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  useSystemConfigStore((state) => state.config.currency.quotaPerUnit)
  const cnyWallet = useQuery({
    queryKey: ['currency-wallet', 'self', 'CNY'],
    queryFn: () => getCurrencyWallet(),
    enabled: props.user !== null,
  })
  if (props.loading) {
    return (
      <div className='grid grid-cols-3 divide-x rounded-lg border'>
        {['balance', 'usage', 'requests'].map((key) => (
          <div key={key} className='min-w-0 px-2.5 py-2.5 sm:px-5 sm:py-4'>
            <Skeleton className='h-3.5 w-full' />
            <Skeleton className='mt-2 h-6 w-full sm:h-7' />
            <Skeleton className='mt-1.5 hidden h-3.5 w-24 md:block' />
          </div>
        ))}
      </div>
    )
  }

  const stats: {
    label: string
    value: string
    description: string
    icon: typeof WalletCards
    tone: IconBadgeTone
  }[] = [
    {
      label: t('USD balance'),
      value: formatBillingQuota(props.user?.quota ?? 0, 'USD', { locale }),
      description: t('Remaining quota'),
      icon: WalletCards,
      tone: 'success',
    },
    {
      label: t('USD consumption'),
      value: formatBillingQuota(props.user?.used_quota ?? 0, 'USD', { locale }),
      description: t('USD model consumption'),
      icon: BarChart3,
      tone: 'info',
    },
    {
      label: t('CNY balance'),
      value: cnyWallet.data
        ? formatBillingQuota(
            cnyWallet.data.quota,
            'CNY',
            { locale },
            cnyWallet.data.quota_per_unit
          )
        : '—',
      description: cnyWallet.isError
        ? t('Failed to load CNY wallet')
        : t('CNY wallet; no exchange rate conversion'),
      icon: WalletCards,
      tone: 'success',
    },
    {
      label: t('CNY consumption'),
      value: cnyWallet.data
        ? formatBillingQuota(
            cnyWallet.data.used_quota,
            'CNY',
            { locale },
            cnyWallet.data.quota_per_unit
          )
        : '—',
      description: t('CNY model consumption'),
      icon: BarChart3,
      tone: 'info',
    },
    {
      label: t('API Requests'),
      value: formatNumber(props.user?.request_count ?? 0, locale),
      description: t('Total requests made'),
      icon: Activity,
      tone: 'chart-4',
    },
  ]

  return (
    <div className='grid grid-cols-2 divide-x rounded-lg border sm:grid-cols-5'>
      {stats.map((item) => (
        <div key={item.label} className='min-w-0 px-2.5 py-2.5 sm:px-5 sm:py-4'>
          <div className='flex items-center gap-1.5 sm:gap-2.5'>
            <IconBadge tone={item.tone} size='stat'>
              <item.icon />
            </IconBadge>
            <div className='text-muted-foreground truncate text-[11px] font-medium tracking-wider uppercase sm:text-xs'>
              {item.label}
            </div>
          </div>

          <div className='text-foreground mt-1.5 font-mono text-sm font-bold tracking-tight break-all tabular-nums sm:mt-2.5 sm:text-2xl'>
            {item.value}
          </div>
          <div className='text-muted-foreground/60 mt-1 hidden text-xs md:block'>
            {item.description}
          </div>
        </div>
      ))}
    </div>
  )
}
