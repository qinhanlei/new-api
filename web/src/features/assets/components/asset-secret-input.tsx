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
import { type ComponentProps, useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { CopyButton } from '@/components/copy-button'
import { Dialog } from '@/components/dialog'
import { LoadingState } from '@/components/loading-state'
import { PasswordInput } from '@/components/password-input'
import { Button } from '@/components/ui/button'
import { Textarea } from '@/components/ui/textarea'
import {
  SecureVerificationDialog,
  useSecureVerification,
} from '@/features/auth/secure-verification'
import { handleServerError } from '@/lib/handle-server-error'
import { ROLE } from '@/lib/roles'
import { AuthOperationError } from '@/lib/secure-verification'
import { useAuthStore } from '@/stores/auth-store'

import { getAssetChannelSecret, getAssetStorageSecret } from '../api'
import type { AssetChannelConfig, AssetStorageConfig } from '../types'

export function AssetSecretInput(
  props: Omit<ComponentProps<typeof PasswordInput>, 'visibilityAction'> & {
    config: AssetStorageConfig | AssetChannelConfig
  }
) {
  const user = useAuthStore((state) => state.auth.user)
  const sessionId = useAuthStore((state) => state.auth.session?.sid)
  const { config, ...inputProps } = props
  const canReveal = user?.role === ROLE.SUPER_ADMIN && config.has_secret
  const channelId = 'channel_id' in config ? config.channel_id : undefined
  return (
    <AssetSecretInputControl
      key={`${user?.id}:${sessionId}:${channelId ?? 'storage'}:${config.revision}:${canReveal}`}
      channelId={channelId}
      canReveal={canReveal}
      inputProps={inputProps}
    />
  )
}

function AssetSecretInputControl(props: {
  channelId?: number
  canReveal: boolean
  inputProps: ComponentProps<typeof PasswordInput>
}) {
  const { t } = useTranslation()
  const verification = useSecureVerification()
  const cancelVerification = verification.cancel
  const operation = useRef<AbortController | null>(null)
  const trigger = useRef<HTMLButtonElement | null>(null)
  const [secret, setSecret] = useState<string | null>(null)
  const [loading, setLoading] = useState(false)

  useEffect(
    () => () => {
      operation.current?.abort()
      operation.current = null
      cancelVerification()
    },
    [cancelVerification]
  )

  const close = () => {
    operation.current?.abort()
    operation.current = null
    cancelVerification()
    setLoading(false)
    setSecret(null)
  }

  const reveal = async () => {
    if (
      !props.canReveal ||
      props.inputProps.value ||
      props.inputProps.disabled ||
      operation.current
    ) {
      return
    }
    const current = new AbortController()
    operation.current = current
    try {
      const target =
        props.channelId === undefined
          ? { scope: 'asset.storage.secret.read' as const }
          : {
              scope: 'channel.assets.secret.read' as const,
              context: { channel_id: props.channelId },
            }
      const proof = await verification.requestVerification({
        ...target,
        title: t('Verify to view saved secret'),
      })
      if (!proof || operation.current !== current) return
      setLoading(true)
      const value =
        props.channelId === undefined
          ? await getAssetStorageSecret(proof.proof_token, current.signal)
          : await getAssetChannelSecret(
              props.channelId,
              proof.proof_token,
              current.signal
            )
      if (operation.current === current) setSecret(value)
    } catch (error) {
      if (operation.current === current && !current.signal.aborted) {
        handleServerError(AuthOperationError.from(error))
      }
    } finally {
      if (operation.current === current) {
        operation.current = null
        setLoading(false)
      }
    }
  }

  return (
    // The verification dialog has its own form; it must not submit settings.
    <div onSubmit={(event) => event.stopPropagation()}>
      <PasswordInput
        {...props.inputProps}
        visibilityAction={
          props.canReveal && !props.inputProps.value
            ? {
                label: t('Verify to view saved secret'),
                disabled: verification.isActive || loading,
                onClick: (event) => {
                  trigger.current = event.currentTarget
                  void reveal()
                },
              }
            : undefined
        }
      />
      <Dialog
        open={loading || secret !== null}
        onOpenChange={(open) => {
          if (!open) close()
        }}
        finalFocus={trigger}
        title={t('Saved secret key')}
        contentClassName='sm:max-w-md'
        showCloseButton={false}
        footer={
          <Button type='button' variant='outline' onClick={close}>
            {t('Close')}
          </Button>
        }
      >
        {loading && <LoadingState className='min-h-20' />}
        {secret !== null && (
          <div className='flex items-start gap-2'>
            <Textarea
              aria-label={t('Saved secret key')}
              value={secret}
              readOnly
              autoComplete='off'
              className='font-mono'
            />
            <CopyButton value={secret} />
          </div>
        )}
      </Dialog>
      <SecureVerificationDialog
        {...verification.dialogProps}
        onCancel={close}
      />
    </div>
  )
}
