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
import { zodResolver } from '@hookform/resolvers/zod'
import { useEffect, useMemo, useRef, useState } from 'react'
import { useFieldArray, useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { Button } from '@/components/ui/button'
import { Combobox } from '@/components/ui/combobox'
import { Field, FieldGroup, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { usePricingData } from '@/features/pricing/hooks/use-pricing-data'
import type { PricingModel } from '@/features/pricing/types'
import { handleServerError } from '@/lib/handle-server-error'

import { useBillingRates } from './api'
import { BillingRateTable } from './billing-rate-table'
import {
  billingRateChanges,
  billingRateInputSchema,
  billingRatesFormSchema,
  type BillingRatesFormValues,
} from './rates'
import type { BillingRateConfig, BillingRateChange } from './types'

export function BillingRatesEditor(props: {
  userId?: number
  modelName?: string
  onDirtyChange?: (dirty: boolean) => void
  onPendingChange?: (pending: boolean) => void
}) {
  const { t } = useTranslation()
  const { query, save, canEdit } = useBillingRates(
    props.userId,
    props.modelName
  )
  const pricing = usePricingData(canEdit)
  if (!canEdit) return null
  if (query.isError && !query.data) {
    return (
      <ErrorState
        description={query.error.message}
        onRetry={() => void query.refetch()}
      />
    )
  }
  if (!query.data) {
    return <LoadingState />
  }
  const billingName = props.modelName
    ? query.data.billing_model_name
    : undefined
  if (props.modelName && !billingName) {
    return (
      <ErrorState
        description={t('Unable to load billing rates')}
        onRetry={() => void query.refetch()}
      />
    )
  }
  return (
    <RateForm
      {...props}
      key={billingName ?? 'all'}
      modelName={billingName}
      config={query.data}
      models={pricing.models}
      save={save}
      reload={async () => (await query.refetch({ throwOnError: true })).data}
    />
  )
}

function RateForm(props: {
  userId?: number
  modelName?: string
  onDirtyChange?: (dirty: boolean) => void
  onPendingChange?: (pending: boolean) => void
  config: BillingRateConfig
  models: PricingModel[]
  save: ReturnType<typeof useBillingRates>['save']
  reload: () => Promise<BillingRateConfig | undefined>
}) {
  const { t } = useTranslation()
  const [snapshot, setSnapshot] = useState(props.config)
  const [selection, setSelection] = useState<Set<string>>(new Set())
  const [modelToAdd, setModelToAdd] = useState('')
  const [batchRate, setBatchRate] = useState('')
  const [pendingChanges, setPendingChanges] = useState<
    BillingRateChange[] | null
  >(null)
  const [filter, setFilter] = useState('')
  const models = useMemo(
    () =>
      new Map(
        props.models.map((model) => [
          model.billing_model_name ?? model.model_name,
          model,
        ])
      ),
    [props.models]
  )
  const initialEntries = props.config.rules
    .filter((rule) =>
      props.modelName ? rule.model_name === props.modelName : rule.rate !== null
    )
    .map((rule) => ({ model_name: rule.model_name, rate: rule.rate ?? '' }))
  if (props.modelName && !initialEntries.length) {
    initialEntries.push({ model_name: props.modelName, rate: '' })
  }
  const form = useForm<BillingRatesFormValues>({
    resolver: zodResolver(billingRatesFormSchema),
    defaultValues: { entries: initialEntries },
  })
  const fieldArray = useFieldArray({ control: form.control, name: 'entries' })
  const entries = form.watch('entries')
  const valid = billingRatesFormSchema.safeParse({ entries }).success
  const changes = valid
    ? billingRateChanges(
        snapshot.rules,
        Object.fromEntries(
          entries.map((entry) => [entry.model_name, entry.rate])
        )
      )
    : []
  const dirty = changes.length > 0 || !valid
  const tooManyChanges = changes.length > 200
  const dirtyCallback = useRef(props.onDirtyChange)
  dirtyCallback.current = props.onDirtyChange
  useEffect(() => {
    dirtyCallback.current?.(dirty)
  }, [dirty])
  useEffect(() => () => dirtyCallback.current?.(false), [])
  const notifyPending = props.onPendingChange
  const pending = props.save.isPending
  useEffect(() => {
    notifyPending?.(pending)
    return () => notifyPending?.(false)
  }, [notifyPending, pending])
  const globalRules = new Map(
    (snapshot.global_rules ?? []).map((rule) => [rule.model_name, rule])
  )
  const displayedRows = fieldArray.fields.flatMap((field, index) =>
    field.model_name.toLowerCase().includes(filter.toLowerCase())
      ? [{ ...field, index }]
      : []
  )
  const persist = async () => {
    if (!pendingChanges) return
    try {
      const data = await props.save.mutateAsync(pendingChanges)
      setSnapshot(data)
      const saved = new Map(
        data.rules.map((rule) => [rule.model_name, rule.rate ?? ''])
      )
      form.reset({
        entries: entries.map((entry) => ({
          ...entry,
          rate: saved.get(entry.model_name) ?? '',
        })),
      })
      setPendingChanges(null)
      toast.success(t('Billing rates saved'))
    } catch (error) {
      handleServerError(error)
    }
  }

  return (
    <section
      aria-label={t(props.userId ? 'User model rates' : 'Global model rates')}
      className='min-w-0 space-y-3'
    >
      <p className='text-muted-foreground text-xs'>
        {t(
          props.userId
            ? 'User rates replace model and group rates. Blank inherits; 0 is free; 1 uses the base price.'
            : 'Model rates replace group rates. User overrides take priority. Blank inherits; 0 is free; 1 uses the base price.'
        )}
      </p>
      {!props.modelName && (
        <FieldGroup className='gap-3'>
          <Field>
            <FieldLabel htmlFor='billing-rate-model'>
              {t('Billing model')}
            </FieldLabel>
            <div className='flex min-w-0 gap-2'>
              <Combobox
                id='billing-rate-model'
                className='min-w-0 flex-1'
                options={[...models.keys()].map((name) => ({
                  value: name,
                  label: name,
                }))}
                value={modelToAdd}
                onValueChange={(value) => setModelToAdd(value ?? '')}
                allowCustomValue
                placeholder={t('Select or enter a billing model')}
                disabled={props.save.isPending}
              />
              <Button
                type='button'
                variant='outline'
                disabled={!modelToAdd.trim() || props.save.isPending}
                onClick={() => {
                  const name = modelToAdd.trim()
                  if (!entries.some((entry) => entry.model_name === name)) {
                    fieldArray.append({
                      model_name: name,
                      rate:
                        snapshot.rules.find((rule) => rule.model_name === name)
                          ?.rate ?? '',
                    })
                  }
                  setFilter('')
                  setModelToAdd('')
                }}
              >
                {t('Add')}
              </Button>
            </div>
          </Field>
          {entries.length > 1 && (
            <Field>
              <FieldLabel htmlFor='billing-rate-search'>
                {t('Search models')}
              </FieldLabel>
              <Input
                id='billing-rate-search'
                value={filter}
                onChange={(event) => setFilter(event.target.value)}
              />
            </Field>
          )}
        </FieldGroup>
      )}
      {selection.size > 0 && (
        <FieldGroup className='gap-2'>
          <Field>
            <FieldLabel htmlFor='billing-batch-rate'>
              {t('Rate for {{count}} selected models', {
                count: selection.size,
              })}
            </FieldLabel>
            <div className='flex flex-wrap gap-2'>
              <Input
                id='billing-batch-rate'
                className='w-32'
                inputMode='decimal'
                value={batchRate}
                onChange={(event) => setBatchRate(event.target.value)}
                placeholder={t('Inherit')}
              />
              <Button
                type='button'
                variant='outline'
                disabled={
                  !billingRateInputSchema.safeParse(batchRate).success ||
                  props.save.isPending
                }
                onClick={() =>
                  entries.forEach((entry, index) => {
                    if (selection.has(entry.model_name)) {
                      form.setValue(`entries.${index}.rate`, batchRate, {
                        shouldDirty: true,
                        shouldValidate: true,
                      })
                    }
                  })
                }
              >
                {t('Apply')}
              </Button>
              <Button
                type='button'
                variant='ghost'
                disabled={props.save.isPending}
                onClick={() =>
                  entries.forEach((entry, index) => {
                    if (selection.has(entry.model_name)) {
                      form.setValue(`entries.${index}.rate`, '', {
                        shouldDirty: true,
                        shouldValidate: true,
                      })
                    }
                  })
                }
              >
                {t('Restore inheritance')}
              </Button>
            </div>
          </Field>
        </FieldGroup>
      )}
      <BillingRateTable
        rows={displayedRows}
        form={form}
        singleModel={Boolean(props.modelName)}
        userSpecific={Boolean(props.userId)}
        disabled={props.save.isPending}
        selection={selection}
        setSelection={setSelection}
        globalRules={globalRules}
        models={models}
      />
      {props.save.isError && (
        <div className='space-y-2'>
          <p role='alert' className='text-destructive text-sm'>
            {props.save.error.message}
          </p>
          <Button
            type='button'
            variant='outline'
            onClick={async () => {
              try {
                const data = await props.reload()
                if (!data) return
                setSnapshot(data)
                form.reset({
                  entries: entries.map((entry) => ({
                    ...entry,
                    rate:
                      data.rules.find(
                        (rule) => rule.model_name === entry.model_name
                      )?.rate ?? '',
                  })),
                })
                setPendingChanges(null)
                props.save.reset()
              } catch (error) {
                handleServerError(error)
              }
            }}
          >
            {t('Reload and discard changes')}
          </Button>
        </div>
      )}
      {tooManyChanges && (
        <p role='alert' className='text-destructive text-sm'>
          {t('Select between 1 and 200 billing rate changes')}
        </p>
      )}
      <div className='flex flex-wrap items-center justify-end gap-2'>
        {dirty && (
          <Button
            type='button'
            variant='ghost'
            disabled={props.save.isPending}
            onClick={() => {
              form.reset({
                entries: entries.map((entry) => ({
                  ...entry,
                  rate:
                    snapshot.rules.find(
                      (rule) => rule.model_name === entry.model_name
                    )?.rate ?? '',
                })),
              })
              props.save.reset()
            }}
          >
            {t('Discard changes')}
          </Button>
        )}
        <Button
          type='button'
          disabled={!dirty || tooManyChanges || props.save.isPending}
          onClick={form.handleSubmit(() => setPendingChanges(changes))}
        >
          {t('Save rates')}
        </Button>
      </div>
      <ConfirmDialog
        open={pendingChanges !== null}
        onOpenChange={(open) => {
          if (!open && !props.save.isPending) setPendingChanges(null)
        }}
        title={t('Save billing rates')}
        desc={t(
          props.userId
            ? 'These rates replace model and group rates for this user. Changes apply to new requests.'
            : 'These rates replace existing group discounts, including free groups. User overrides remain effective. Changes apply to new requests.'
        )}
        confirmText={t('Save rates')}
        isLoading={props.save.isPending}
        handleConfirm={() => void persist()}
      >
        <ul className='max-h-48 overflow-y-auto text-sm'>
          {pendingChanges?.map((change) => (
            <li key={change.model_name} className='break-all'>
              <span className='font-mono'>{change.model_name}</span>:{' '}
              {snapshot.rules.find(
                (rule) => rule.model_name === change.model_name
              )?.rate ?? t('Inherit')}{' '}
              → {change.rate ?? t('Inherit')}
            </li>
          ))}
        </ul>
        {props.save.isError && (
          <p role='alert' className='text-destructive text-sm'>
            {props.save.error.message}
          </p>
        )}
      </ConfirmDialog>
    </section>
  )
}
