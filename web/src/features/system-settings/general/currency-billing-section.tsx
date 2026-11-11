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
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import type { Resolver } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Form } from '@/components/ui/form'
import { formatTimestampToDate } from '@/lib/format'
import { handleServerError } from '@/lib/handle-server-error'
import { ROLE } from '@/lib/roles'
import {
  getServerErrorMessage,
  getServerErrorSources,
} from '@/lib/server-error-message'
import { useAuthStore } from '@/stores/auth-store'

import { FormNavigationGuard } from '../components/form-navigation-guard'
import { SettingsCard } from '../components/settings-card'
import { SettingsForm } from '../components/settings-form-layout'
import { SettingsPageFormActions } from '../components/settings-page-context'
import { useSettingsForm } from '../hooks/use-settings-form'
import {
  getCurrencyBillingSettings,
  previewCurrencyBillingRate,
  updateCurrencyBillingSettings,
  type CurrencyBillingSettings,
} from './currency-billing-api'
import { CurrencyBillingFields } from './currency-billing-fields'
import { CurrencyBillingStatus } from './currency-billing-status'
import {
  createCurrencyBillingSchema,
  getCurrencyBillingFormValues,
  type CurrencyBillingFormValues,
} from './lib/currency-billing-form'

export function CurrencyBillingSection() {
  const { t } = useTranslation()
  const isAdmin = useAuthStore(
    (state) => (state.auth.user?.role ?? 0) >= ROLE.ADMIN
  )
  const settings = useQuery({
    queryKey: ['currency-billing'],
    queryFn: getCurrencyBillingSettings,
    enabled: isAdmin,
    refetchOnWindowFocus: false,
    retry: false,
    meta: { errorToast: false },
  })
  if (!isAdmin) return null
  return (
    <section aria-label={t('Wallet currency billing')}>
      <SettingsCard title={t('Wallet currency billing')}>
        {!settings.data && settings.isError && (
          <ErrorState
            className='min-h-0 py-6'
            title={t('Failed to load currency settings')}
            description={getServerErrorMessage(settings.error)}
            onRetry={() => void settings.refetch()}
          />
        )}
        {!settings.data && !settings.isError && (
          <LoadingState message={t('Loading settings...')} />
        )}
        {settings.data && (
          <CurrencyBillingForm
            settings={settings.data}
            onReload={async () => {
              const result = await settings.refetch()
              if (result.isError) throw result.error
              return result.data
            }}
          />
        )}
      </SettingsCard>
    </section>
  )
}

function CurrencyBillingForm(props: {
  settings: CurrencyBillingSettings
  onReload: () => Promise<CurrencyBillingSettings | undefined>
}) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [saved, setSaved] = useState(props.settings)
  const [saveError, setSaveError] = useState<unknown>(null)
  const [reloading, setReloading] = useState(false)
  const update = useMutation({
    mutationFn: updateCurrencyBillingSettings,
    meta: { errorToast: false },
  })
  const schema = createCurrencyBillingSchema(t)
  const { form, handleSubmit, handleReset, isDirty, isSubmitting } =
    useSettingsForm<CurrencyBillingFormValues>({
      resolver: zodResolver(schema) as Resolver<
        CurrencyBillingFormValues,
        unknown,
        CurrencyBillingFormValues
      >,
      defaultValues: getCurrencyBillingFormValues(saved.policy),
      onSubmit: async (values) => {
        if (
          values.dynamic_enabled &&
          !saved.policy.dynamic_enabled &&
          (!preview.data || preview.isFetching || preview.isError)
        ) {
          throw new Error(t('Failed to fetch exchange rate'))
        }
        setSaveError(null)
        try {
          const result = await update.mutateAsync({
            policy: { ...saved.policy, ...values },
            ...(values.dynamic_enabled && !saved.policy.dynamic_enabled
              ? { baseline_rate: preview.data?.rate }
              : {}),
          })
          setSaved(result)
          queryClient.setQueryData(['currency-billing'], result)
          toast.success(t('Setting updated successfully'))
        } catch (error) {
          setSaveError(error)
          handleServerError(error, t('Failed to update setting'))
          throw error
        }
      },
    })
  const dynamic = form.watch('dynamic_enabled')
  const needsBaseline = dynamic && !saved.policy.dynamic_enabled
  const preview = useQuery({
    queryKey: ['currency-billing', 'preview'],
    queryFn: previewCurrencyBillingRate,
    enabled: needsBaseline,
    retry: false,
    staleTime: 0,
    refetchOnWindowFocus: false,
    meta: { errorToast: false },
  })
  const saving = update.isPending || isSubmitting || reloading
  const save = () => handleSubmit().catch(() => undefined)
  const policyConflict = getServerErrorSources(saveError).some(
    (source) =>
      source.message === 'Currency settings have changed; reload before saving'
  )

  return (
    <>
      <FormNavigationGuard when={isDirty} />
      <Form {...form}>
        <SettingsForm
          noValidate
          onSubmit={(event) => {
            event.preventDefault()
            void save()
          }}
        >
          <CurrencyBillingFields disabled={saving} />
          {needsBaseline && (
            <div className='space-y-2'>
              {preview.isFetching && (
                <LoadingState
                  inline
                  size='sm'
                  message={t('Fetching exchange rate...')}
                />
              )}
              {preview.data && !preview.isError && (
                <div
                  role='status'
                  aria-label={t('Initial dynamic exchange rate')}
                  className='space-y-1 text-sm'
                >
                  <p>
                    {t('Initial dynamic exchange rate')}: 1 USD ={' '}
                    {preview.data.rate} CNY
                  </p>
                  <p className='text-muted-foreground text-xs'>
                    Frankfurter ·{' '}
                    {formatTimestampToDate(preview.data.source_time)}
                  </p>
                  <p className='text-muted-foreground text-xs'>
                    {t('Saving confirms this rate as the initial baseline.')}
                  </p>
                </div>
              )}
              {preview.isError && (
                <p role='alert' className='text-destructive text-sm'>
                  {getServerErrorMessage(
                    preview.error,
                    t('Failed to fetch exchange rate')
                  )}
                </p>
              )}
              <Button
                type='button'
                variant='outline'
                size='sm'
                disabled={saving || preview.isFetching}
                onClick={() => void preview.refetch()}
              >
                {t('Refresh rate preview')}
              </Button>
            </div>
          )}
          <CurrencyBillingStatus settings={props.settings} />
          {saveError != null && (
            <Alert variant='destructive'>
              <AlertDescription>
                {getServerErrorMessage(
                  saveError,
                  t('Failed to update setting')
                )}
                {policyConflict && (
                  <Button
                    type='button'
                    variant='outline'
                    size='sm'
                    disabled={saving}
                    onClick={async () => {
                      setReloading(true)
                      try {
                        const latest = await props.onReload()
                        if (latest) {
                          setSaved(latest)
                          form.reset(
                            getCurrencyBillingFormValues(latest.policy)
                          )
                          setSaveError(null)
                        }
                      } catch (error) {
                        setSaveError(error)
                        handleServerError(
                          error,
                          t('Failed to load currency settings')
                        )
                      } finally {
                        setReloading(false)
                      }
                    }}
                  >
                    {t('Reload settings')}
                  </Button>
                )}
              </AlertDescription>
            </Alert>
          )}
          <SettingsPageFormActions
            inline
            onSave={save}
            onReset={() => {
              handleReset()
              setSaveError(null)
            }}
            isSaving={saving}
            isResetDisabled={!isDirty}
            isSaveDisabled={
              !isDirty ||
              (needsBaseline &&
                (!preview.data || preview.isFetching || preview.isError))
            }
          />
        </SettingsForm>
      </Form>
    </>
  )
}
