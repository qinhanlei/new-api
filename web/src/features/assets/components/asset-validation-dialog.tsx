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
import { toast } from 'sonner'

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
import {
  getServerErrorMessage,
  getServerErrorStatus,
  isServerErrorCancelled,
  translateServerText,
} from '@/lib/server-error-message'

import {
  assetQueryKeys,
  executeAssetAction,
  getAssetValidationSessions,
} from '../api'
import { assetGroupSchema } from '../lib/schemas'
import {
  assetValidationH5Link,
  assetValidationRetryAt,
} from '../lib/validation'
import type {
  AssetScope,
  AssetValidationSummary,
  AssetValidationView,
} from '../types'
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
  const [now, setNow] = useState(Date.now)
  const [polling, setPolling] = useState({
    nextAt: 0,
    until: 0,
    failures: 0,
    blocked: false,
    binding: false,
    expiryChecked: false,
  })
  const activeSession = useRef('')
  const completedSession = useRef('')
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
  function receiveSession(result: AssetValidationView, expectedId?: string) {
    void client.invalidateQueries({
      queryKey: [...assetQueryKeys.all, 'validation'],
    })
    const ready = result.Status === 'created' && result.GroupId
    if (ready) void client.invalidateQueries({ queryKey: assetQueryKeys.all })
    if (
      !mounted.current ||
      (expectedId && activeSession.current !== expectedId)
    ) {
      return
    }
    const receivedAt = Date.now()
    activeSession.current = result.BytedToken
    setSession(result)
    setNow(receivedAt)
    setPolling((previous) => {
      const binding =
        previous.binding ||
        (result.CanPoll &&
          (result.Status === 'unknown' ||
            result.ExpiresAt * 1000 <= receivedAt))
      let until = previous.until || result.ExpiresAt * 1000
      if (binding && !previous.binding) until = receivedAt + 10 * 60_000
      return {
        nextAt: ready
          ? 0
          : Math.max(receivedAt + 10_000, (result.NextPollAt || 0) * 1000),
        until,
        failures: 0,
        blocked: false,
        binding,
        expiryChecked: previous.expiryChecked,
      }
    })
    if (
      ready &&
      result.GroupId &&
      completedSession.current !== result.BytedToken
    ) {
      completedSession.current = result.BytedToken
      toast.success(t('Verification completed. The person group is ready.'))
      props.onCreated(result.GroupId)
    }
  }

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
    onSuccess: (result) => receiveSession(result),
    onError: (error) => {
      if (!mounted.current || getServerErrorStatus(error) !== 429) return
      const failedAt = Date.now()
      setNow(failedAt)
      setPolling((previous) => ({
        ...previous,
        nextAt: Math.max(
          failedAt + 10_000,
          assetValidationRetryAt(error, failedAt)
        ),
      }))
    },
    retry: false,
    gcTime: 0,
    meta: { errorToast: false },
  })
  const check = useMutation({
    mutationFn: (sessionId: string) =>
      executeAssetAction<AssetValidationView>(
        'GetVisualValidateResult',
        { ProjectName: props.project, BytedToken: sessionId },
        props.scope
      ),
    onSuccess: (result, sessionId) => receiveSession(result, sessionId),
    onError: (error, sessionId) => {
      if (!mounted.current || activeSession.current !== sessionId) return
      const failedAt = Date.now()
      const status = getServerErrorStatus(error)
      const retry =
        !isServerErrorCancelled(error) &&
        (status === undefined ||
          status === 408 ||
          status === 429 ||
          status >= 500)
      setNow(failedAt)
      setPolling((previous) => ({
        ...previous,
        blocked: !retry,
        failures: previous.failures + 1,
        nextAt: retry
          ? Math.max(
              failedAt + Math.min(10_000 * 2 ** previous.failures, 60_000),
              assetValidationRetryAt(error, failedAt)
            )
          : 0,
      }))
      if (status === 401 || status === 403 || status === 404) {
        setSession(
          (previous) =>
            previous && {
              ...previous,
              H5Link: undefined,
              CallbackURL: undefined,
            }
        )
      }
    },
    retry: false,
    gcTime: 0,
    meta: { errorToast: false },
  })
  const checkSession = check.mutate
  const sessionId = session?.BytedToken
  const status = session?.Status
  const expiresAt = (session?.ExpiresAt || 0) * 1000
  const canPoll = session?.CanPoll === true && status !== 'created'
  const nextCheckAt = Math.max(
    polling.nextAt,
    (session?.NextPollAt || 0) * 1000
  )
  const canCheckExpiry =
    canPoll && !polling.binding && !polling.expiryChecked && expiresAt > 0
  const autoChecking =
    canPoll &&
    !polling.blocked &&
    (now < polling.until || (canCheckExpiry && now >= expiresAt))
  const cooldown = nextCheckAt > now

  useEffect(() => {
    const timestamp = Date.now()
    let wakeAt = Infinity
    if (nextCheckAt > timestamp) wakeAt = nextCheckAt
    if (expiresAt > timestamp) wakeAt = Math.min(wakeAt, expiresAt)
    if (canPoll && polling.until > timestamp) {
      wakeAt = Math.min(wakeAt, polling.until)
    }
    if (
      sessionId &&
      canPoll &&
      !polling.blocked &&
      !check.isPending &&
      (timestamp < polling.until || (canCheckExpiry && timestamp >= expiresAt))
    ) {
      wakeAt = Math.min(wakeAt, nextCheckAt)
    }
    if (!Number.isFinite(wakeAt)) return
    // One timer owns polling, cooldowns and link expiry, including in-flight checks.
    const timer = window.setTimeout(
      () => {
        const time = Date.now()
        setNow(time)
        if (
          sessionId &&
          activeSession.current === sessionId &&
          canPoll &&
          !polling.blocked &&
          !check.isPending &&
          time >= nextCheckAt &&
          (time < polling.until || (canCheckExpiry && time >= expiresAt))
        ) {
          if (canCheckExpiry && time >= expiresAt) {
            // Refresh once at expiry: the server may already hold a group to bind.
            setPolling((previous) => ({ ...previous, expiryChecked: true }))
          }
          checkSession(sessionId)
        }
      },
      Math.min(Math.max(0, wakeAt - timestamp), 2_147_483_647)
    )
    return () => window.clearTimeout(timer)
  }, [
    sessionId,
    canPoll,
    canCheckExpiry,
    nextCheckAt,
    expiresAt,
    polling.until,
    polling.blocked,
    check.isPending,
    checkSession,
    now,
  ])

  function continueSession(item: AssetValidationSummary) {
    const time = Date.now()
    const binding =
      item.CanPoll &&
      (item.Status === 'unknown' || item.ExpiresAt * 1000 <= time)
    activeSession.current = item.SessionId
    setSession({ ...item, BytedToken: item.SessionId })
    setNow(time)
    setPolling({
      nextAt: (item.NextPollAt || 0) * 1000,
      until: binding ? time + 10 * 60_000 : item.ExpiresAt * 1000,
      failures: 0,
      blocked: false,
      binding,
      expiryChecked: false,
    })
    create.reset()
    check.reset()
    if (!item.NextPollAt || item.NextPollAt * 1000 <= time) {
      check.mutate(item.SessionId)
    }
  }

  const expired = Boolean(session && expiresAt <= now)
  const link =
    !expired &&
    !polling.blocked &&
    (status === 'pending' || status === 'verifying')
      ? assetValidationH5Link(
          session?.H5Link,
          i18n.resolvedLanguage || i18n.language
        )
      : ''
  const error = create.error || check.error
  const sessionError = session?.Error
    ? `${session.Error.Code}: ${translateServerText(t, session.Error.Message)}`
    : ''
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
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
            {(status === 'expired' || expired) && !canPoll && (
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
            {sessionError && !check.error && (
              <p
                role={status === 'failed' ? 'alert' : 'status'}
                className={
                  status === 'failed'
                    ? 'text-destructive text-sm break-words'
                    : 'text-muted-foreground text-sm break-words'
                }
              >
                {status === 'failed'
                  ? sessionError
                  : t('Last check: {{message}}', { message: sessionError })}
              </p>
            )}
            {canPoll &&
              !polling.blocked &&
              !autoChecking &&
              !check.isPending &&
              now >= polling.until && (
                <p role='status' className='text-muted-foreground text-sm'>
                  {t(
                    'Automatic checks paused. You can check this verification again.'
                  )}
                </p>
              )}
            <div className='flex flex-wrap gap-2'>
              <Button
                variant='outline'
                disabled={check.isPending || cooldown || status === 'created'}
                onClick={() => {
                  if (Date.now() < nextCheckAt) return
                  setPolling((previous) => ({
                    ...previous,
                    blocked: false,
                    until: previous.binding
                      ? Date.now() + 10 * 60_000
                      : session.ExpiresAt * 1000,
                  }))
                  check.mutate(session.BytedToken)
                }}
              >
                {check.isPending ? t('Checking...') : t('Check verification')}
              </Button>
              <Button
                variant='ghost'
                disabled={check.isPending}
                onClick={() => {
                  activeSession.current = ''
                  setSession(null)
                  setPolling({
                    nextAt: 0,
                    until: 0,
                    failures: 0,
                    blocked: false,
                    binding: false,
                    expiryChecked: false,
                  })
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
                  if (Date.now() < nextCheckAt) return
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
                  disabled={create.isPending || check.isPending || cooldown}
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
                      onClick={() => continueSession(item)}
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
          <p
            role={autoChecking ? 'status' : 'alert'}
            className={
              autoChecking
                ? 'text-muted-foreground text-sm break-words'
                : 'text-destructive text-sm break-words'
            }
          >
            {getServerErrorStatus(error) === 429
              ? t('Too many requests. Please wait before trying again.')
              : getServerErrorMessage(error)}
          </p>
        )}
        {cooldown && (
          <p role='status' className='text-muted-foreground text-sm'>
            {autoChecking
              ? t('Next check at {{time}}', {
                  time: new Date(nextCheckAt).toLocaleTimeString(locale),
                })
              : t('Try again after {{time}}', {
                  time: new Date(nextCheckAt).toLocaleTimeString(locale),
                })}
          </p>
        )}
      </div>
    </Dialog>
  )
}
