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
import { useId, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { Button } from '@/components/ui/button'
import {
  Field,
  FieldContent,
  FieldDescription,
  FieldLabel,
} from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Switch } from '@/components/ui/switch'
import { api } from '@/lib/api'
import {
  billingAmountToQuota,
  billingQuotaToAmount,
  formatBillingQuota,
} from '@/lib/currency'
import { handleServerError } from '@/lib/handle-server-error'
import { requireServerSuccess } from '@/lib/server-error-message'

type TokenCurrencyBudget = {
  enabled: boolean
  remain_quota: number
  unlimited_quota: boolean
  used_quota: number
  version: number
  quota_per_unit: number
}

export function TokenCurrencyBudget(props: { tokenId: number; open: boolean }) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const id = useId()
  const [draft, setDraft] = useState<TokenCurrencyBudget | null>(null)
  const [saving, setSaving] = useState(false)
  const [failed, setFailed] = useState(false)
  const budget = useQuery({
    queryKey: ['token-currency-budget', props.tokenId, 'CNY'],
    queryFn: async (): Promise<TokenCurrencyBudget> =>
      requireServerSuccess(
        (await api.get(`/api/token/${props.tokenId}/currency/CNY`)).data
      ).data,
    enabled: props.open,
    refetchOnWindowFocus: false,
  })
  const values = draft ?? budget.data
  const save = async () => {
    if (
      !values ||
      !Number.isSafeInteger(values.remain_quota) ||
      values.remain_quota < 0
    ) {
      return
    }
    setSaving(true)
    try {
      const updated = requireServerSuccess(
        (
          await api.put(`/api/token/${props.tokenId}/currency/CNY`, {
            enabled: values.enabled,
            remain_quota: values.remain_quota,
            unlimited_quota: values.unlimited_quota,
            expected_version: values.version,
          })
        ).data
      ).data as TokenCurrencyBudget
      queryClient.setQueryData(
        ['token-currency-budget', props.tokenId, 'CNY'],
        updated
      )
      setDraft(null)
      setFailed(false)
      toast.success(t('CNY token budget saved'))
    } catch (error) {
      setFailed(true)
      handleServerError(
        error,
        t(
          'Failed to save CNY token budget. Reload the latest balance before retrying.'
        )
      )
    } finally {
      setSaving(false)
    }
  }
  return (
    <div className='space-y-3 rounded-lg border p-3'>
      <p className='text-sm font-medium'>{t('CNY token budget')}</p>
      <p className='text-muted-foreground text-xs'>{t('Saved separately.')}</p>
      {!values && budget.isError && (
        <ErrorState
          className='min-h-0 py-3'
          title={t('Failed to load CNY token budget')}
          action={
            <Button
              type='button'
              variant='outline'
              size='sm'
              onClick={() => budget.refetch()}
            >
              {t('Retry')}
            </Button>
          }
        />
      )}
      {!values && !budget.isError && (
        <LoadingState inline size='sm' message={t('Loading...')} />
      )}
      {values && (
        <>
          <Field orientation='horizontal'>
            <FieldLabel htmlFor={`${id}-enabled`}>
              {t('Enable CNY billing')}
            </FieldLabel>
            <Switch
              id={`${id}-enabled`}
              disabled={saving}
              checked={values.enabled}
              onCheckedChange={(enabled) => setDraft({ ...values, enabled })}
            />
          </Field>
          <Field orientation='horizontal'>
            <FieldContent>
              <FieldLabel htmlFor={`${id}-unlimited`}>
                {t('Unlimited CNY quota for this key')}
              </FieldLabel>
              <FieldDescription
                id={`${id}-unlimited-description`}
                className='text-xs'
              >
                {t(
                  'Limits CNY-priced model usage. Wallet funds are still required.'
                )}
              </FieldDescription>
            </FieldContent>
            <Switch
              id={`${id}-unlimited`}
              aria-describedby={`${id}-unlimited-description`}
              disabled={saving || !values.enabled}
              checked={values.unlimited_quota}
              onCheckedChange={(unlimited_quota) =>
                setDraft({ ...values, unlimited_quota })
              }
            />
          </Field>
          {!values.unlimited_quota && (
            <Field>
              <FieldLabel htmlFor={`${id}-amount`}>
                {t('Remaining CNY quota')}
              </FieldLabel>
              <Input
                id={`${id}-amount`}
                type='number'
                min={0}
                step={0.000002}
                value={billingQuotaToAmount(
                  values.remain_quota,
                  'CNY',
                  values.quota_per_unit
                )}
                disabled={saving || !values.enabled}
                onChange={(event) =>
                  setDraft({
                    ...values,
                    remain_quota: billingAmountToQuota(
                      Number(event.target.value),
                      'CNY',
                      values.quota_per_unit
                    ),
                  })
                }
              />
            </Field>
          )}
          <p className='text-muted-foreground text-xs'>
            {t('CNY consumption')}:{' '}
            {formatBillingQuota(
              values.used_quota,
              'CNY',
              undefined,
              values.quota_per_unit
            )}
          </p>
          <div className='flex gap-2'>
            <Button
              type='button'
              size='sm'
              disabled={
                saving ||
                !draft ||
                failed ||
                !Number.isSafeInteger(values.remain_quota) ||
                values.remain_quota < 0
              }
              onClick={save}
            >
              {t('Save CNY budget')}
            </Button>
            <Button
              type='button'
              size='sm'
              variant='outline'
              disabled={saving || budget.isFetching}
              onClick={async () => {
                const latest = await budget.refetch()
                if (latest.isSuccess) {
                  setDraft(null)
                  setFailed(false)
                }
              }}
            >
              {t('Reload balance')}
            </Button>
          </div>
        </>
      )}
      {failed && (
        <p role='alert' className='text-destructive text-xs'>
          {t(
            'Failed to save CNY token budget. Reload the latest balance before retrying.'
          )}
        </p>
      )}
    </div>
  )
}
