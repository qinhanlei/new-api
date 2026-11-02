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
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from '@/components/ui/collapsible'
import {
  Form,
  FormControl,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { NativeSelect } from '@/components/ui/native-select'
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
  getAssetValidationChannels,
  getAssetValidationRecord,
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
  initialSession?: AssetValidationSummary
  onOpenRecords?: () => void
  onClose: () => void
  onCreated: (groupId: string) => void
}) {
  const { t, i18n } = useTranslation()
  const client = useQueryClient()
  const id = useId()
  const [activeView, setSession] = useState<AssetValidationView | null>(null)
  const [selectedChannel, setSelectedChannel] = useState(0)
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
  const request = useRef<{
    name: string
    channelId: number
    key: string
  } | null>(null)
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
  const record = useQuery({
    queryKey: [
      ...assetQueryKeys.all,
      'validation',
      'record',
      props.project,
      props.scope,
      props.initialSession?.SessionId,
    ],
    queryFn: ({ signal }) =>
      getAssetValidationRecord(
        props.initialSession?.SessionId ?? '',
        props.project,
        props.scope,
        signal
      ),
    enabled: Boolean(props.initialSession) && !activeView,
    meta: { errorToast: false },
  })
  const initial = record.data ?? props.initialSession
  const session: AssetValidationView | null =
    activeView ??
    (initial ? { ...initial, BytedToken: initial.SessionId } : null)
  const restoring = Boolean(initial && !activeView)
  const channels = useQuery({
    queryKey: [
      ...assetQueryKeys.all,
      'validation',
      'channels',
      props.project,
      props.scope,
    ],
    queryFn: ({ signal }) =>
      getAssetValidationChannels(props.project, props.scope, signal),
    enabled: !session,
    meta: { errorToast: false },
  })
  const channelId =
    channels.data?.length === 1 ? channels.data[0].Id : selectedChannel
  const channelAvailable =
    channels.data?.some((item) => item.Id === channelId) === true
  function receiveSession(result: AssetValidationView, expectedId?: string) {
    void client.invalidateQueries({
      queryKey: [...assetQueryKeys.all, 'validation'],
    })
    const ready = Boolean(result.GroupId)
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
      const binding = previous.binding || result.BindingPending
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
      if (
        !request.current ||
        request.current.name !== values.name ||
        request.current.channelId !== channelId
      ) {
        request.current = { name: values.name, channelId, key: nanoid() }
      }
      return executeAssetAction<AssetValidationView>(
        'CreateVisualValidateSession',
        {
          ProjectName: props.project,
          Name: values.name,
          ChannelId: channelId,
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
              CanContinue: false,
              CanPoll: false,
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
  const status = session?.GroupId ? 'created' : session?.Status
  const expiresAt = (session?.ExpiresAt || 0) * 1000
  const canContinue =
    session?.CanContinue === true && status !== 'created' && !polling.blocked
  const canPoll = !restoring && canContinue && session?.CanPoll === true
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
    if (!item.CanContinue || item.GroupId || !record.isSuccess) return
    const time = Date.now()
    activeSession.current = item.SessionId
    setSession({ ...item, BytedToken: item.SessionId })
    setNow(time)
    setPolling({
      nextAt: (item.NextPollAt || 0) * 1000,
      until: item.BindingPending ? time + 10 * 60_000 : item.ExpiresAt * 1000,
      failures: 0,
      blocked: false,
      binding: item.BindingPending,
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
    !expired && canContinue && (status === 'pending' || status === 'verifying')
      ? assetValidationH5Link(
          session?.H5Link,
          i18n.resolvedLanguage || i18n.language
        )
      : ''
  const error = create.error || check.error
  const errorStatus = getServerErrorStatus(error)
  const transientError =
    Boolean(error) &&
    (errorStatus === undefined ||
      errorStatus === 408 ||
      errorStatus === 429 ||
      (errorStatus !== undefined && errorStatus >= 500))
  const sessionError = session?.Error
    ? translateServerText(t, session.Error.Message)
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
        <>
          {props.onOpenRecords && (
            <Button
              variant='ghost'
              onClick={props.onOpenRecords}
              disabled={create.isPending}
            >
              {t('Verification records')}
            </Button>
          )}
          <Button
            variant='outline'
            onClick={props.onClose}
            disabled={create.isPending}
          >
            {t('Close')}
          </Button>
        </>
      }
    >
      <div className='space-y-4'>
        {session ? (
          <div className='space-y-3'>
            <div className='flex flex-wrap items-center gap-2'>
              <p className='min-w-0 font-medium break-words'>{session.Name}</p>
              <AssetValidationStatus
                status={status ?? session.Status}
                needsReview={session.NeedsReview}
                bindingPending={session.BindingPending}
              />
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
            <dl className='grid grid-cols-[auto_minmax(0,1fr)] gap-x-3 gap-y-1 text-sm'>
              <dt className='text-muted-foreground'>{t('Project')}</dt>
              <dd className='break-words'>{props.project}</dd>
              <dt className='text-muted-foreground'>
                {t('Verification channel')}
              </dt>
              <dd className='break-words'>
                {session.ChannelName || session.ChannelId}
              </dd>
              <dt className='text-muted-foreground'>{t('Last checked')}</dt>
              <dd>
                {session.LastCheckedAt
                  ? new Date(session.LastCheckedAt * 1000).toLocaleString(
                      locale
                    )
                  : t('Not recorded')}
              </dd>
            </dl>
            {restoring && record.isPending && <LoadingState />}
            {restoring && record.isError && (
              <ErrorState
                description={getServerErrorMessage(record.error)}
                onRetry={() => void record.refetch()}
              />
            )}
            {session.ContinueError && status !== 'created' && (
              <p role='status' className='text-muted-foreground text-sm'>
                {translateServerText(t, session.ContinueError.Message)}
              </p>
            )}
            {session.NeedsReview && status !== 'created' && (
              <p role='status' className='text-muted-foreground text-sm'>
                {t(
                  'The link cannot be resumed, and the provider result is unconfirmed. Check the record before starting again.'
                )}
              </p>
            )}
            {session.BindingPending && status !== 'created' && (
              <p role='status' className='text-muted-foreground text-sm'>
                {t(
                  'The provider result is saved. Linking can continue after the verification link expires.'
                )}
              </p>
            )}
            {(status === 'expired' || expired) &&
              !session.NeedsReview &&
              !session.BindingPending &&
              status !== 'created' &&
              !canPoll && (
                <p className='text-muted-foreground text-sm'>
                  {t('This verification link has expired.')}
                </p>
              )}
            {check.isPending && (
              <p role='status' className='text-muted-foreground text-sm'>
                {t('Checking...')}
              </p>
            )}
            {check.isSuccess &&
              canContinue &&
              !session.BindingPending &&
              !session.NeedsReview &&
              !check.isPending && (
                <p role='status' className='text-muted-foreground text-sm'>
                  {t(
                    'Verification is not complete yet. Finish the phone steps and wait for the next check.'
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
            {session.Error && (
              <Collapsible className='text-muted-foreground text-xs'>
                <CollapsibleTrigger
                  render={<Button variant='ghost' size='sm' />}
                >
                  {t('Diagnostic details')}
                </CollapsibleTrigger>
                <CollapsibleContent>
                  <p className='mt-2 break-all'>{session.Error.Code}</p>
                </CollapsibleContent>
              </Collapsible>
            )}
            <div className='flex flex-wrap gap-2'>
              {status === 'created' && session.GroupId && (
                <Button
                  onClick={() => {
                    if (session.GroupId) props.onCreated(session.GroupId)
                  }}
                >
                  {t('View person group')}
                </Button>
              )}
              {restoring && canContinue && initial && (
                <Button
                  disabled={!record.isSuccess || record.isFetching || cooldown}
                  aria-label={t('Continue verification for {{name}}', {
                    name: session.Name,
                  })}
                  onClick={() => continueSession(initial)}
                >
                  {t('Continue verification')}
                </Button>
              )}
              {!restoring &&
                (canContinue || check.isPending || Boolean(check.error)) &&
                status !== 'created' && (
                  <Button
                    variant='outline'
                    disabled={!canContinue || check.isPending || cooldown}
                    onClick={() => {
                      if (!canContinue || Date.now() < nextCheckAt) return
                      setPolling((previous) => ({
                        ...previous,
                        until: previous.binding
                          ? Date.now() + 10 * 60_000
                          : session.ExpiresAt * 1000,
                      }))
                      check.mutate(session.BytedToken)
                    }}
                  >
                    {check.isPending
                      ? t('Checking...')
                      : t('Check verification')}
                  </Button>
                )}
            </div>
            {status !== 'created' && (
              <p className='text-muted-foreground text-xs'>
                {t(
                  'Closing pauses automatic checks. Your verification remains in Verification records.'
                )}
              </p>
            )}
          </div>
        ) : (
          <>
            <p className='text-muted-foreground text-sm'>
              {t('Project')}:{' '}
              <span className='text-foreground'>{props.project}</span>
            </p>
            <div className='space-y-2'>
              <Label htmlFor={`${id}-channel`}>
                {t('Verification channel')}
              </Label>
              <NativeSelect
                id={`${id}-channel`}
                className='w-full'
                value={channelId || ''}
                disabled={
                  channels.isPending ||
                  create.isPending ||
                  channels.data?.length === 1
                }
                onChange={(event) =>
                  setSelectedChannel(Number(event.target.value))
                }
              >
                <option value=''>{t('Select a channel')}</option>
                {channels.data?.map((channel) => (
                  <option key={channel.Id} value={channel.Id}>
                    {channel.Name}
                  </option>
                ))}
              </NativeSelect>
              {channels.isPending && <LoadingState />}
              {channels.isError && (
                <ErrorState
                  description={getServerErrorMessage(channels.error)}
                  onRetry={() => void channels.refetch()}
                />
              )}
              {channels.isSuccess && !channels.data.length && (
                <p role='status' className='text-muted-foreground text-sm'>
                  {t('No material channel is available for human verification')}
                </p>
              )}
            </div>
            <Form {...form}>
              <form
                id={id}
                className='space-y-3'
                onSubmit={form.handleSubmit((values) => {
                  if (!channelAvailable || Date.now() < nextCheckAt) return
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
                  disabled={
                    !channelAvailable ||
                    channels.isFetching ||
                    create.isPending ||
                    check.isPending ||
                    cooldown
                  }
                >
                  {create.isPending
                    ? t('Creating...')
                    : t('Start verification')}
                </Button>
              </form>
            </Form>
          </>
        )}
        {error && (
          <p
            role={transientError ? 'status' : 'alert'}
            className={
              transientError
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
