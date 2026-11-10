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
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Dialog } from '@/components/dialog'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import {
  getCurrencyWallet,
  adjustCurrencyWallet,
} from '@/features/wallet/currency-api'
import {
  getCurrencyDisplay,
  getCurrencyLabel,
  formatBillingQuota,
  type BillingCurrency,
} from '@/lib/currency'
import { formatQuota, parseQuotaFromDollars } from '@/lib/format'
import { handleServerError } from '@/lib/handle-server-error'
import { cn } from '@/lib/utils'

import { adjustUserQuota } from '../api'
import type { QuotaAdjustMode } from '../types'

interface UserQuotaDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  userId: number
  currentQuota: number
  onSuccess: () => void
}

export function UserQuotaDialog(props: UserQuotaDialogProps) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [mode, setMode] = useState<QuotaAdjustMode>('add')
  const [amount, setAmount] = useState('')
  const [loading, setLoading] = useState(false)
  const [billingCurrency, setBillingCurrency] = useState<BillingCurrency>('USD')
  const [reason, setReason] = useState('')
  const adjustmentAttempt = useRef<{
    fingerprint: string
    operationId: string
  } | null>(null)
  const cnyWallet = useQuery({
    queryKey: ['currency-wallet', props.userId, 'CNY'],
    queryFn: () => getCurrencyWallet(props.userId),
    enabled: props.open && billingCurrency === 'CNY',
  })

  const { meta: currencyMeta } = getCurrencyDisplay()
  const currencyLabel = billingCurrency === 'CNY' ? 'CNY' : getCurrencyLabel()
  const tokensOnly = billingCurrency === 'USD' && currencyMeta.kind === 'tokens'

  const amountValue = Number.parseFloat(amount) || 0
  const quotaValue =
    billingCurrency === 'CNY'
      ? Math.round(Math.abs(amountValue) * 500000)
      : parseQuotaFromDollars(Math.abs(amountValue))

  const getPreviewText = () => {
    if (billingCurrency === 'CNY') {
      if (!cnyWallet.data) {
        return t(cnyWallet.isError ? 'Failed to load CNY wallet' : 'Loading...')
      }
      const delta = mode === 'subtract' ? -quotaValue : quotaValue
      return `${formatBillingQuota(cnyWallet.data.quota, 'CNY')} → ${formatBillingQuota(cnyWallet.data.quota + delta, 'CNY')}`
    }
    const current = props.currentQuota
    const val = quotaValue
    switch (mode) {
      case 'add':
        return `${t('Current quota')}: ${formatQuota(current)}  +${formatQuota(val)} = ${formatQuota(current + val)}`
      case 'subtract':
        return `${t('Current quota')}: ${formatQuota(current)}  -${formatQuota(val)} = ${formatQuota(current - val)}`
      case 'override': {
        const overrideQuota = parseQuotaFromDollars(amountValue)
        return `${t('Current quota')}: ${formatQuota(current)} → ${formatQuota(overrideQuota)}`
      }
      default:
        return ''
    }
  }

  const handleConfirm = async () => {
    if (!amount && mode !== 'override') return
    if (quotaValue <= 0 && mode !== 'override') return

    setLoading(true)
    try {
      if (billingCurrency === 'CNY') {
        if (
          !reason.trim() ||
          !Number.isSafeInteger(quotaValue) ||
          quotaValue <= 0
        ) {
          return
        }
        const delta = mode === 'subtract' ? -quotaValue : quotaValue
        const fingerprint = JSON.stringify([props.userId, delta, reason.trim()])
        if (adjustmentAttempt.current?.fingerprint !== fingerprint) {
          adjustmentAttempt.current = {
            fingerprint,
            operationId: crypto.randomUUID(),
          }
        }
        const updated = await adjustCurrencyWallet(props.userId, {
          delta_quota: delta,
          operation_id: adjustmentAttempt.current.operationId,
          reason: reason.trim(),
        })
        queryClient.setQueryData(
          ['currency-wallet', props.userId, 'CNY'],
          updated
        )
        queryClient.invalidateQueries({
          queryKey: ['currency-wallet', 'self', 'CNY'],
        })
        adjustmentAttempt.current = null
        toast.success(t('Quota adjusted successfully'))
        setAmount('')
        setReason('')
        props.onOpenChange(false)
        props.onSuccess()
        return
      }
      const value =
        mode === 'override' ? parseQuotaFromDollars(amountValue) : quotaValue
      const result = await adjustUserQuota({
        id: props.userId,
        action: 'add_quota',
        mode,
        value: mode === 'override' ? value : Math.abs(value),
      })
      if (result.success) {
        toast.success(t('Quota adjusted successfully'))
        setAmount('')
        setMode('add')
        props.onOpenChange(false)
        props.onSuccess()
      } else {
        handleServerError(result, t('Failed to adjust quota'))
      }
    } catch (e: unknown) {
      handleServerError(e, t('Failed to adjust quota'))
    } finally {
      setLoading(false)
    }
  }

  const handleCancel = () => {
    setAmount('')
    setMode('add')
    props.onOpenChange(false)
  }

  const placeholder = tokensOnly
    ? t('Enter amount in tokens')
    : t('Enter amount in {{currency}}', { currency: currencyLabel })

  return (
    <Dialog
      open={props.open}
      onOpenChange={props.onOpenChange}
      title={t('Adjust Quota')}
      description={t('Select an operation mode and enter the amount')}
      contentHeight='auto'
      bodyClassName='space-y-4'
      footer={
        <>
          <Button variant='outline' onClick={handleCancel}>
            {t('Cancel')}
          </Button>
          <Button
            onClick={handleConfirm}
            disabled={
              loading ||
              (billingCurrency === 'CNY' &&
                (!cnyWallet.data || !reason.trim() || quotaValue <= 0))
            }
          >
            {loading ? t('Processing...') : t('Confirm')}
          </Button>
        </>
      }
    >
      <div className='space-y-4'>
        <div className='space-y-2'>
          <Label>{t('Billing currency')}</Label>
          <Select
            value={billingCurrency}
            items={[
              { value: 'USD', label: 'USD' },
              { value: 'CNY', label: 'CNY' },
            ]}
            disabled={loading}
            onValueChange={(value) => {
              if (value === 'USD' || value === 'CNY') {
                setBillingCurrency(value)
                setAmount('')
                setMode('add')
              }
            }}
          >
            <SelectTrigger aria-label={t('Billing currency')}>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value='USD'>USD</SelectItem>
              <SelectItem value='CNY'>CNY</SelectItem>
            </SelectContent>
          </Select>
        </div>
        <div className='text-muted-foreground text-sm'>{getPreviewText()}</div>

        <div className='space-y-2'>
          <Label>{t('Mode')}</Label>
          <div className='flex gap-1'>
            {(['add', 'subtract', 'override'] as const)
              .filter((m) => billingCurrency === 'USD' || m !== 'override')
              .map((m) => (
                <Button
                  key={m}
                  type='button'
                  variant='outline'
                  size='sm'
                  disabled={loading}
                  className={cn(
                    mode === m &&
                      'bg-primary text-primary-foreground hover:bg-primary/90 hover:text-primary-foreground'
                  )}
                  onClick={() => {
                    setMode(m)
                    setAmount('')
                  }}
                >
                  {m === 'add' && t('Add')}
                  {!(m === 'add') && m === 'subtract' && t('Subtract')}
                  {!(m === 'add') && !(m === 'subtract') && t('Override')}
                </Button>
              ))}
          </div>
        </div>

        <div className='space-y-2'>
          <Label>
            {t('Amount')} ({currencyLabel})
          </Label>
          <Input
            aria-label={t('Amount')}
            type='number'
            step={tokensOnly ? 1 : 0.000001}
            min={mode === 'override' ? undefined : 0}
            placeholder={placeholder}
            value={amount}
            disabled={loading}
            onChange={(e) => setAmount(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter') handleConfirm()
            }}
          />
        </div>
        {billingCurrency === 'CNY' && (
          <div className='space-y-2'>
            <Label htmlFor='cny-adjustment-reason'>{t('Reason')}</Label>
            <Input
              id='cny-adjustment-reason'
              value={reason}
              disabled={loading}
              onChange={(event) => setReason(event.target.value)}
              maxLength={255}
            />
          </div>
        )}
      </div>
    </Dialog>
  )
}
