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
import { nanoid } from 'nanoid'
import { useId, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Dialog } from '@/components/dialog'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import {
  getCurrencyWallet,
  adjustCurrencyWallet,
} from '@/features/wallet/currency-api'
import {
  billingAmountToQuota,
  formatBillingQuota,
  type BillingCurrency,
} from '@/lib/currency'
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
  const id = useId()
  const queryClient = useQueryClient()
  const [mode, setMode] = useState<QuotaAdjustMode>('add')
  const [amount, setAmount] = useState('')
  const [loading, setLoading] = useState(false)
  const [billingCurrency, setBillingCurrency] = useState<BillingCurrency>('USD')
  const submitting = useRef(false)
  const adjustmentAttempt = useRef<{
    fingerprint: string
    operationId: string
  } | null>(null)
  const cnyWallet = useQuery({
    queryKey: ['currency-wallet', props.userId, 'CNY'],
    queryFn: () => getCurrencyWallet(props.userId),
    enabled: props.open && billingCurrency === 'CNY',
  })

  const quotaPerUnit =
    billingCurrency === 'CNY' ? cnyWallet.data?.quota_per_unit : undefined
  const amountValue = Number(amount)
  const quotaValue = billingAmountToQuota(
    mode === 'override' ? amountValue : Math.abs(amountValue),
    billingCurrency,
    quotaPerUnit
  )
  const validAmount =
    Number.isFinite(amountValue) &&
    Number.isSafeInteger(quotaValue) &&
    (mode === 'override' || quotaValue > 0)
  const walletReady = billingCurrency === 'USD' || !!cnyWallet.data

  const getPreviewText = () => {
    if (billingCurrency === 'CNY' && !cnyWallet.data) {
      return t(cnyWallet.isError ? 'Failed to load CNY wallet' : 'Loading...')
    }
    const current =
      billingCurrency === 'CNY'
        ? (cnyWallet.data?.quota ?? 0)
        : props.currentQuota
    const options = { digitsLarge: 2, digitsSmall: 4, abbreviate: true }
    const currentDisplay = formatBillingQuota(
      current,
      billingCurrency,
      options,
      quotaPerUnit
    )
    const amountDisplay = formatBillingQuota(
      quotaValue,
      billingCurrency,
      options,
      quotaPerUnit
    )
    switch (mode) {
      case 'add':
        return `${t('Current quota')}: ${currentDisplay}  +${amountDisplay} = ${formatBillingQuota(current + quotaValue, billingCurrency, options, quotaPerUnit)}`
      case 'subtract':
        return `${t('Current quota')}: ${currentDisplay}  -${amountDisplay} = ${formatBillingQuota(current - quotaValue, billingCurrency, options, quotaPerUnit)}`
      case 'override':
        return `${t('Current quota')}: ${currentDisplay} → ${amountDisplay}`
      default:
        return ''
    }
  }

  const handleConfirm = async () => {
    if (submitting.current || !validAmount || !walletReady) return

    submitting.current = true
    setLoading(true)
    try {
      if (billingCurrency === 'CNY') {
        const fingerprint = JSON.stringify([props.userId, mode, quotaValue])
        if (adjustmentAttempt.current?.fingerprint !== fingerprint) {
          adjustmentAttempt.current = {
            fingerprint,
            operationId: `cny-${nanoid()}`,
          }
        }
        const updated = await adjustCurrencyWallet(props.userId, {
          mode,
          value: quotaValue,
          operation_id: adjustmentAttempt.current.operationId,
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
        setMode('add')
        props.onOpenChange(false)
        props.onSuccess()
        return
      }
      const result = await adjustUserQuota({
        id: props.userId,
        action: 'add_quota',
        mode,
        value: quotaValue,
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
      submitting.current = false
      setLoading(false)
    }
  }

  const handleCancel = () => {
    setAmount('')
    setMode('add')
    props.onOpenChange(false)
  }

  const placeholder = t('Enter amount in {{currency}}', {
    currency: billingCurrency,
  })

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
            disabled={loading || !validAmount || !walletReady}
          >
            {loading ? t('Processing...') : t('Confirm')}
          </Button>
        </>
      }
    >
      <div className='space-y-4'>
        <div className='space-y-2'>
          <Label htmlFor={`${id}-currency`}>{t('Billing currency')}</Label>
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
            <SelectTrigger
              id={`${id}-currency`}
              aria-label={t('Billing currency')}
            >
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectGroup>
                <SelectItem value='USD'>USD</SelectItem>
                <SelectItem value='CNY'>CNY</SelectItem>
              </SelectGroup>
            </SelectContent>
          </Select>
        </div>
        <div className='text-muted-foreground text-sm'>{getPreviewText()}</div>

        <div className='space-y-2'>
          <Label>{t('Mode')}</Label>
          <div className='flex gap-1'>
            {(['add', 'subtract', 'override'] as const).map((m) => (
              <Button
                key={m}
                type='button'
                variant='outline'
                size='sm'
                disabled={loading}
                aria-pressed={mode === m}
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
          <Label htmlFor={`${id}-amount`}>
            {t('Amount')} ({billingCurrency})
          </Label>
          <Input
            id={`${id}-amount`}
            aria-label={t('Amount')}
            type='number'
            step={0.000001}
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
      </div>
    </Dialog>
  )
}
