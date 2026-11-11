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
import type { Dispatch, SetStateAction } from 'react'
import type { UseFormReturn } from 'react-hook-form'
import { useTranslation } from 'react-i18next'

import { StaticDataTable } from '@/components/data-table'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Field, FieldDescription } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { ModelPriceCell } from '@/features/pricing/components/model-price-cell'
import type { PricingModel } from '@/features/pricing/types'

import { billingRateInputSchema, type BillingRatesFormValues } from './rates'
import type { BillingRateRule } from './types'

export function BillingRateTable(props: {
  rows: { id: string; model_name: string; index: number }[]
  form: UseFormReturn<BillingRatesFormValues>
  singleModel: boolean
  userSpecific: boolean
  disabled: boolean
  selection: Set<string>
  setSelection: Dispatch<SetStateAction<Set<string>>>
  globalRules: Map<string, BillingRateRule>
  models: Map<string, PricingModel>
}) {
  const { t } = useTranslation()
  return (
    <StaticDataTable
      data={props.rows}
      getRowKey={(row) => row.id}
      tableProps={{ 'aria-label': t('Billing rates') }}
      emptyContent={t('Add a model to set its rate')}
      columns={[
        ...(!props.singleModel
          ? [
              {
                id: 'select',
                header: (
                  <Checkbox
                    aria-label={t('Select all visible models')}
                    checked={
                      props.rows.length > 0 &&
                      props.rows.every((row) =>
                        props.selection.has(row.model_name)
                      )
                    }
                    onCheckedChange={(checked) =>
                      props.setSelection((previous) => {
                        const next = new Set(previous)
                        for (const row of props.rows) {
                          if (checked) next.add(row.model_name)
                          else next.delete(row.model_name)
                        }
                        return next
                      })
                    }
                  />
                ),
                cell: (row: (typeof props.rows)[number]) => (
                  <Checkbox
                    aria-label={t('Select {{model}}', {
                      model: row.model_name,
                    })}
                    checked={props.selection.has(row.model_name)}
                    onCheckedChange={(checked) =>
                      props.setSelection((previous) => {
                        const next = new Set(previous)
                        if (checked) next.add(row.model_name)
                        else next.delete(row.model_name)
                        return next
                      })
                    }
                  />
                ),
              },
            ]
          : []),
        {
          id: 'model',
          header: t('Billing model'),
          cellClassName:
            'min-w-40 max-w-72 whitespace-normal break-all align-top',
          cell: (row) => (
            <div>
              <span className='font-mono text-xs'>{row.model_name}</span>
              {props.userSpecific && (
                <p className='text-muted-foreground mt-1 text-xs'>
                  {t('Global model rate')}:{' '}
                  {props.globalRules.get(row.model_name)?.rate ?? t('Inherit')}
                </p>
              )}
            </div>
          ),
        },
        {
          id: 'rate',
          header: t('Rate'),
          cellClassName: 'min-w-36 align-top',
          cell: (row) => (
            <Field
              data-invalid={Boolean(
                props.form.formState.errors.entries?.[row.index]?.rate
              )}
            >
              <Input
                aria-label={t('Rate for {{model}}', {
                  model: row.model_name,
                })}
                inputMode='decimal'
                className='w-28'
                placeholder={t('Inherit')}
                disabled={props.disabled}
                aria-invalid={Boolean(
                  props.form.formState.errors.entries?.[row.index]?.rate
                )}
                {...props.form.register(`entries.${row.index}.rate`)}
              />
              {props.form.formState.errors.entries?.[row.index]?.rate && (
                <FieldDescription className='text-destructive max-w-44 whitespace-normal'>
                  {t(
                    'Enter a rate from 0 to 100 with up to six decimal places, or leave blank to inherit.'
                  )}
                </FieldDescription>
              )}
              <Button
                type='button'
                size='sm'
                variant='ghost'
                disabled={
                  props.disabled ||
                  props.form.watch(`entries.${row.index}`)?.rate === ''
                }
                onClick={() =>
                  props.form.setValue(`entries.${row.index}.rate`, '', {
                    shouldDirty: true,
                    shouldValidate: true,
                  })
                }
              >
                {t('Inherit')}
              </Button>
            </Field>
          ),
        },
        {
          id: 'preview',
          header: t('Base price → Rate price'),
          cellClassName: 'min-w-48 align-top',
          cell: (row) => {
            const model = props.models.get(row.model_name)
            if (!model) {
              return (
                <span className='text-muted-foreground text-xs'>
                  {t('Price preview unavailable')}
                </span>
              )
            }
            const raw = props.form.watch(`entries.${row.index}`)?.rate ?? ''
            const rate =
              raw.trim() === '' && props.userSpecific
                ? props.globalRules.get(row.model_name)?.rate
                : raw
            const base = {
              ...model,
              group_ratio: { base: 1 },
              effective_rates: undefined,
              enable_groups: ['base'],
            }
            return (
              <div className='space-y-2'>
                <ModelPriceCell
                  model={base}
                  options={{ tokenUnit: 'M' }}
                  showExpression={false}
                />
                {rate != null &&
                rate !== '' &&
                billingRateInputSchema.safeParse(rate).success ? (
                  <div className='border-t pt-2'>
                    <ModelPriceCell
                      model={{ ...base, group_ratio: { base: Number(rate) } }}
                      options={{ tokenUnit: 'M' }}
                      showExpression={false}
                    />
                  </div>
                ) : (
                  <p className='text-muted-foreground text-xs'>
                    {t('Uses the existing group rate')}
                  </p>
                )}
              </div>
            )
          },
        },
      ]}
    />
  )
}
