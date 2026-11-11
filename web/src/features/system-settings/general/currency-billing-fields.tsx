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
import { useState } from 'react'
import { useFormContext, useWatch } from 'react-hook-form'
import { useTranslation } from 'react-i18next'

import { Accordion } from '@/components/ui/accordion'
import {
  FormControl,
  FormDescription,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { Input } from '@/components/ui/input'
import { Switch } from '@/components/ui/switch'

import { SettingsAccordion } from '../components/settings-accordion'
import {
  SettingsFormGrid,
  SettingsSwitchContent,
  SettingsSwitchItem,
} from '../components/settings-form-layout'
import type { CurrencyBillingFormValues } from './lib/currency-billing-form'

export function CurrencyBillingFields(props: { disabled: boolean }) {
  const { t } = useTranslation()
  const form = useFormContext<CurrencyBillingFormValues>()
  const dynamic = useWatch({ control: form.control, name: 'dynamic_enabled' })
  const interval = useWatch({ control: form.control, name: 'interval_hours' })
  const [expanded, setExpanded] = useState<string[]>([])
  const errors = form.formState.errors
  const hasAdvancedErrors = !!(
    errors.max_quote_age_hours ||
    errors.max_change_percent ||
    errors.max_week_change_percent
  )
  const showDynamicFields =
    dynamic || hasAdvancedErrors || !!errors.interval_hours
  return (
    <>
      <FormField
        control={form.control}
        name='cross_currency_enabled'
        render={({ field }) => (
          <SettingsSwitchItem>
            <SettingsSwitchContent>
              <FormLabel>{t('Cross-currency wallet payment')}</FormLabel>
              <FormDescription>
                {t(
                  'When the model currency balance is insufficient, pay the shortfall from the other wallet at the current rate.'
                )}
              </FormDescription>
            </SettingsSwitchContent>
            <FormControl>
              <Switch
                checked={field.value}
                onCheckedChange={field.onChange}
                disabled={props.disabled}
              />
            </FormControl>
          </SettingsSwitchItem>
        )}
      />
      <FormField
        control={form.control}
        name='dynamic_enabled'
        render={({ field }) => (
          <SettingsSwitchItem>
            <SettingsSwitchContent>
              <FormLabel>{t('Dynamic exchange rate')}</FormLabel>
              <FormDescription>
                {t(
                  'Fetch the USD/CNY reference rate from Frankfurter. Rejected updates keep the last valid rate until it expires.'
                )}
              </FormDescription>
            </SettingsSwitchContent>
            <FormControl>
              <Switch
                checked={field.value}
                onCheckedChange={field.onChange}
                disabled={props.disabled}
              />
            </FormControl>
          </SettingsSwitchItem>
        )}
      />
      {(!dynamic || form.formState.errors.static_rate) && (
        <FormField
          control={form.control}
          name='static_rate'
          render={({ field }) => (
            <FormItem>
              <FormLabel>{t('Wallet exchange rate (CNY per USD)')}</FormLabel>
              <FormControl>
                <Input
                  type='number'
                  min='0.01'
                  max='1000'
                  step='any'
                  {...field}
                  disabled={props.disabled}
                />
              </FormControl>
              <FormDescription>
                {t(
                  '1 USD = this amount of CNY. Used only for wallet payments.'
                )}
              </FormDescription>
              <FormMessage />
            </FormItem>
          )}
        />
      )}
      {showDynamicFields && (
        <>
          <FormField
            control={form.control}
            name='interval_hours'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Update interval (hours)')}</FormLabel>
                <FormControl>
                  <Input
                    type='number'
                    min={1}
                    max={168}
                    step={1}
                    {...field}
                    disabled={props.disabled}
                  />
                </FormControl>
                <FormMessage />
              </FormItem>
            )}
          />
          <Accordion
            value={hasAdvancedErrors ? ['safeguards'] : expanded}
            onValueChange={setExpanded}
          >
            <SettingsAccordion
              value='safeguards'
              title={t('Exchange rate safeguards')}
              keepMounted
            >
              <SettingsFormGrid>
                <FormField
                  control={form.control}
                  name='max_quote_age_hours'
                  render={({ field }) => (
                    <FormItem>
                      <FormLabel>{t('Maximum rate age (hours)')}</FormLabel>
                      <FormControl>
                        <Input
                          type='number'
                          min={interval}
                          max={720}
                          step={1}
                          {...field}
                          disabled={props.disabled}
                        />
                      </FormControl>
                      <FormDescription>
                        {t(
                          'After expiry, cross-currency payments stop until a valid rate is available.'
                        )}
                      </FormDescription>
                      <FormMessage />
                    </FormItem>
                  )}
                />
                <FormField
                  control={form.control}
                  name='max_change_percent'
                  render={({ field }) => (
                    <FormItem>
                      <FormLabel>{t('Maximum daily change (%)')}</FormLabel>
                      <FormControl>
                        <Input
                          type='number'
                          min={0}
                          max={50}
                          step='any'
                          {...field}
                          disabled={props.disabled}
                        />
                      </FormControl>
                      <FormMessage />
                    </FormItem>
                  )}
                />
                <FormField
                  control={form.control}
                  name='max_week_change_percent'
                  render={({ field }) => (
                    <FormItem>
                      <FormLabel>{t('Maximum weekly change (%)')}</FormLabel>
                      <FormControl>
                        <Input
                          type='number'
                          min={0}
                          max={50}
                          step='any'
                          {...field}
                          disabled={props.disabled}
                        />
                      </FormControl>
                      <FormMessage />
                    </FormItem>
                  )}
                />
              </SettingsFormGrid>
            </SettingsAccordion>
          </Accordion>
        </>
      )}
    </>
  )
}
