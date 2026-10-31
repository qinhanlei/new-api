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
import { nanoid } from 'nanoid'
import { QRCodeSVG } from 'qrcode.react'
import { useEffect, useId, useRef, useState } from 'react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'

import { CopyButton } from '@/components/copy-button'
import { Dialog } from '@/components/dialog'
import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { Button } from '@/components/ui/button'
import {
  Form,
  FormControl,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { Input } from '@/components/ui/input'
import { toIntlLocale } from '@/i18n/languages'
import { getServerErrorMessage } from '@/lib/server-error-message'

import {
  assetQueryKeys,
  executeAssetAction,
  getAssetValidationSessions,
} from '../api'
import { assetGroupSchema } from '../lib/schemas'
import { assetValidationH5Link } from '../lib/validation'
import type { AssetScope, AssetValidationView } from '../types'
import { AssetValidationStatus } from './asset-validation-status'

export function AssetValidationDialog(props: {
  project: string
  scope: AssetScope
  onClose: () => void
  onCreated: (groupId: string) => void
}) {
  const { t, i18n } = useTranslation()
  const client = useQueryClient()
  const id = useId()
  const [session, setSession] = useState<AssetValidationView | null>(null)
  const [expiredSession, setExpiredSession] = useState('')
  const request = useRef<{ name: string; key: string } | null>(null)
  const mounted = useRef(false)
  useEffect(() => {
    mounted.current = true
    return () => {
      mounted.current = false
    }
  }, [])
  const form = useForm<{ name: string }>({
    resolver: zodResolver(assetGroupSchema(t)),
    defaultValues: { name: '' },
  })
  const sessions = useQuery({
    queryKey: [...assetQueryKeys.all, 'validation', props.project, props.scope],
    queryFn: ({ signal }) =>
      getAssetValidationSessions(props.project, props.scope, signal),
    refetchInterval: 10_000,
    meta: { errorToast: false },
  })
  const create = useMutation({
    mutationFn: (values: { name: string }) => {
      if (!request.current || request.current.name !== values.name) {
        request.current = { name: values.name, key: nanoid() }
      }
      return executeAssetAction<AssetValidationView>(
        'CreateVisualValidateSession',
        {
          ProjectName: props.project,
          Name: values.name,
          IdempotencyKey: request.current.key,
        },
        props.scope
      )
    },
    onSuccess: (result) => {
      void client.invalidateQueries({
        queryKey: [...assetQueryKeys.all, 'validation'],
      })
      if (result.Status === 'created' && result.GroupId) {
        void client.invalidateQueries({ queryKey: assetQueryKeys.all })
      }
      if (!mounted.current) return
      setSession(result)
      if (result.Status === 'created' && result.GroupId) {
        props.onCreated(result.GroupId)
      }
    },
    retry: false,
    gcTime: 0,
    meta: { errorToast: false },
  })
  const check = useMutation({
    mutationFn: (sessionId: string) =>
      executeAssetAction<AssetValidationView>(
        'GetVisualValidateResult',
        {
          ProjectName: props.project,
          BytedToken: sessionId,
        },
        props.scope
      ),
    onSuccess: (result) => {
      void client.invalidateQueries({
        queryKey: [...assetQueryKeys.all, 'validation'],
      })
      if (result.Status === 'created' && result.GroupId) {
        void client.invalidateQueries({ queryKey: assetQueryKeys.all })
      }
      if (!mounted.current) return
      setSession(result)
      if (result.Status === 'created' && result.GroupId) {
        props.onCreated(result.GroupId)
      }
    },
    retry: false,
    gcTime: 0,
    meta: { errorToast: false },
  })
  const checkSession = check.mutate
  const sessionId = session?.BytedToken
  const status = session?.Status
  useEffect(() => {
    if (
      !sessionId ||
      (status !== 'creating' &&
        status !== 'pending' &&
        status !== 'verifying') ||
      check.isPending ||
      check.isError
    ) {
      return
    }
    const timer = window.setTimeout(() => checkSession(sessionId), 10_000)
    return () => window.clearTimeout(timer)
  }, [sessionId, status, check.isPending, check.isError, checkSession])
  const expiresAt = session?.ExpiresAt
  useEffect(() => {
    if (!sessionId || !expiresAt) return
    const delay = expiresAt * 1000 - Date.now()
    if (delay <= 0) return
    const timer = window.setTimeout(
      () => setExpiredSession(sessionId),
      Math.min(delay, 2_147_483_647)
    )
    return () => window.clearTimeout(timer)
  }, [sessionId, expiresAt])
  const expired = Boolean(
    session &&
    (expiredSession === sessionId || session.ExpiresAt <= Date.now() / 1000)
  )
  const link =
    !expired && (status === 'pending' || status === 'verifying')
      ? assetValidationH5Link(
          session?.H5Link,
          i18n.resolvedLanguage || i18n.language
        )
      : ''
  const error = create.error || check.error
  return (
    <Dialog
      open
      title={t('Verify a person')}
      description={t(
        'Each group belongs to one person and stays on its verification channel.'
      )}
      onOpenChange={(open) => !open && !create.isPending && props.onClose()}
      footer={
        <Button
          variant='outline'
          onClick={props.onClose}
          disabled={create.isPending}
        >
          {t('Close')}
        </Button>
      }
    >
      <div className='space-y-4'>
        {session ? (
          <div className='space-y-3'>
            <div className='flex flex-wrap items-center gap-2'>
              <p className='min-w-0 font-medium break-words'>{session.Name}</p>
              <AssetValidationStatus status={session.Status} />
            </div>
            {link && (
              <>
                <p className='text-muted-foreground text-sm'>
                  {t(
                    'Open the link or scan with your phone. Return here after verification to finish creating the group.'
                  )}
                </p>
                <div className='mx-auto w-fit rounded-lg bg-white p-3'>
                  <QRCodeSVG
                    value={link}
                    size={192}
                    title={t('Verification QR code')}
                  />
                </div>
                <div className='flex flex-wrap justify-center gap-2'>
                  <Button
                    render={
                      <a
                        href={link}
                        target='_blank'
                        rel='noopener noreferrer'
                        referrerPolicy='no-referrer'
                      />
                    }
                  >
                    {t('Open verification')}
                  </Button>
                  <CopyButton
                    value={link}
                    variant='outline'
                    size='default'
                    aria-label={t('Copy verification link')}
                  >
                    {t('Copy link')}
                  </CopyButton>
                </div>
                <p className='text-muted-foreground text-center text-xs'>
                  {t('Link expires at {{time}}', {
                    time: new Date(session.ExpiresAt * 1000).toLocaleTimeString(
                      toIntlLocale(i18n.resolvedLanguage || i18n.language)
                    ),
                  })}
                </p>
              </>
            )}
            {(status === 'expired' || expired) && (
              <p className='text-muted-foreground text-sm'>
                {t(
                  'This verification link has expired. Start a new verification if no group was created.'
                )}
              </p>
            )}
            {status === 'unknown' && (
              <p className='text-muted-foreground text-sm'>
                {t(
                  'The provider result is unconfirmed. Check again before starting a new verification.'
                )}
              </p>
            )}
            {session.Error && (
              <p role='alert' className='text-destructive text-sm break-words'>
                {session.Error.Code}: {t(session.Error.Message)}
              </p>
            )}
            <div className='flex flex-wrap gap-2'>
              <Button
                variant='outline'
                disabled={check.isPending}
                onClick={() => check.mutate(session.BytedToken)}
              >
                {check.isPending ? t('Checking...') : t('Check verification')}
              </Button>
              <Button
                variant='ghost'
                disabled={check.isPending}
                onClick={() => {
                  setSession(null)
                  create.reset()
                  check.reset()
                  request.current = null
                }}
              >
                {t('Back')}
              </Button>
            </div>
          </div>
        ) : (
          <>
            <Form {...form}>
              <form
                id={id}
                className='space-y-3'
                onSubmit={form.handleSubmit((values) => {
                  check.reset()
                  create.mutate(values)
                })}
              >
                <FormField
                  control={form.control}
                  name='name'
                  render={({ field }) => (
                    <FormItem>
                      <FormLabel>{t('Name')}</FormLabel>
                      <FormControl>
                        <Input
                          {...field}
                          autoFocus
                          maxLength={64}
                          disabled={create.isPending || check.isPending}
                        />
                      </FormControl>
                      <FormMessage />
                    </FormItem>
                  )}
                />
                <Button
                  type='submit'
                  disabled={create.isPending || check.isPending}
                >
                  {create.isPending
                    ? t('Creating...')
                    : t('Start verification')}
                </Button>
              </form>
            </Form>
            {sessions.isPending && <LoadingState />}
            {sessions.isError && (
              <ErrorState
                description={getServerErrorMessage(sessions.error)}
                onRetry={() => void sessions.refetch()}
              />
            )}
            {Boolean(sessions.data?.length) && (
              <section
                className='space-y-2'
                aria-label={t('Recent verifications')}
              >
                <h3 className='text-sm font-medium'>
                  {t('Recent verifications')}
                </h3>
                {sessions.data?.map((item) => (
                  <div
                    key={item.SessionId}
                    className='flex flex-wrap items-center gap-2 rounded-lg border p-3'
                  >
                    <span className='min-w-0 flex-1 text-sm break-words'>
                      {item.Name}
                    </span>
                    <AssetValidationStatus status={item.Status} />
                    <Button
                      variant='outline'
                      size='sm'
                      disabled={check.isPending || create.isPending}
                      aria-label={t('Continue verification for {{name}}', {
                        name: item.Name,
                      })}
                      onClick={() => {
                        create.reset()
                        check.mutate(item.SessionId)
                      }}
                    >
                      {t('Continue')}
                    </Button>
                  </div>
                ))}
              </section>
            )}
          </>
        )}
        {error && (
          <p role='alert' className='text-destructive text-sm break-words'>
            {getServerErrorMessage(error)}
          </p>
        )}
      </div>
    </Dialog>
  )
}
