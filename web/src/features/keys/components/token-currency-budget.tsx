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
import { useId } from 'react'
import { useController, type UseFormReturn } from 'react-hook-form'
import { useTranslation } from 'react-i18next'

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
import {
  billingAmountToQuota,
  billingQuotaToAmount,
  formatBillingQuota,
} from '@/lib/currency'

import type { ApiKeyFormValues } from '../lib/api-key-form'
import type { CNYTokenBudget } from '../types'

type TokenCurrencyBudgetProps = {
  form: UseFormReturn<ApiKeyFormValues>
  budget?: CNYTokenBudget
  loading: boolean
  loadFailed: boolean
  saveFailed: boolean
  disabled: boolean
  onReload?: () => void
}

export function TokenCurrencyBudget(props: TokenCurrencyBudgetProps) {
  const { t } = useTranslation()
  const id = useId()
  const { field } = useController({
    control: props.form.control,
    name: 'cny_budget',
  })
  const values = field.value
  const quotaPerUnit = props.budget?.quota_per_unit
  const amountError =
    props.form.formState.errors.cny_budget?.remain_quota?.message

  return (
    <div className='space-y-3 rounded-lg border p-3'>
      <p className='text-sm font-medium'>{t('CNY token budget')}</p>
      {props.loadFailed && (
        <ErrorState
          className='min-h-0 py-3'
          title={t('Failed to load CNY token budget')}
          onRetry={props.onReload}
        />
      )}
      {!props.loadFailed && props.loading && (
        <LoadingState inline size='sm' message={t('Loading...')} />
      )}
      {!props.loadFailed && !props.loading && (
        <>
          <Field orientation='horizontal'>
            <FieldLabel htmlFor={`${id}-enabled`}>
              {t('Enable CNY billing')}
            </FieldLabel>
            <Switch
              id={`${id}-enabled`}
              ref={field.ref}
              checked={values.enabled}
              onCheckedChange={(enabled) =>
                field.onChange({ ...values, enabled })
              }
              onBlur={field.onBlur}
              disabled={props.disabled}
            />
          </Field>
          <Field orientation='horizontal'>
            <FieldContent>
              <FieldLabel htmlFor={`${id}-unlimited`}>
                {t('Unlimited CNY quota for this key')}
              </FieldLabel>
              <FieldDescription id={`${id}-description`} className='text-xs'>
                {t(
                  'Limits CNY-priced model usage. Wallet funds are still required.'
                )}
              </FieldDescription>
            </FieldContent>
            <Switch
              id={`${id}-unlimited`}
              aria-describedby={`${id}-description`}
              checked={values.unlimited_quota}
              onCheckedChange={(unlimited_quota) =>
                field.onChange({ ...values, unlimited_quota })
              }
              onBlur={field.onBlur}
              disabled={props.disabled || !values.enabled}
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
                  quotaPerUnit
                )}
                disabled={props.disabled || !values.enabled}
                onBlur={field.onBlur}
                aria-invalid={!!amountError}
                aria-describedby={
                  amountError ? `${id}-amount-error` : undefined
                }
                onChange={(event) =>
                  field.onChange({
                    ...values,
                    remain_quota: billingAmountToQuota(
                      Number(event.target.value),
                      'CNY',
                      quotaPerUnit
                    ),
                  })
                }
              />
            </Field>
          )}
          {amountError && (
            <p
              id={`${id}-amount-error`}
              role='alert'
              className='text-destructive text-xs'
            >
              {amountError}
            </p>
          )}
          {props.budget && (
            <p className='text-muted-foreground text-xs'>
              {t('CNY consumption')}:{' '}
              {formatBillingQuota(
                props.budget.used_quota,
                'CNY',
                undefined,
                quotaPerUnit
              )}
            </p>
          )}
          {props.onReload && (
            <Button
              type='button'
              size='sm'
              variant='outline'
              disabled={props.disabled}
              onClick={props.onReload}
            >
              {t('Reload balance')}
            </Button>
          )}
        </>
      )}
      {props.saveFailed && (
        <p role='alert' className='text-destructive text-xs'>
          {t(
            'Failed to save CNY token budget. Reload the latest balance before retrying.'
          )}
        </p>
      )}
    </div>
  )
}
