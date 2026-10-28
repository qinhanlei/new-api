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
import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { CopyButton } from '@/components/copy-button'
import { Dialog } from '@/components/dialog'
import { LoadingState } from '@/components/loading-state'
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

export function AssetSecretDisclosure(props: {
  config: AssetStorageConfig | AssetChannelConfig
  disabled?: boolean
}) {
  const user = useAuthStore((state) => state.auth.user)
  const sessionId = useAuthStore((state) => state.auth.session?.sid)
  if (user?.role !== ROLE.SUPER_ADMIN || !props.config.has_secret) return null
  const channelId =
    'channel_id' in props.config ? props.config.channel_id : undefined
  return (
    <AssetSecretDisclosureControl
      key={`${user.id}:${sessionId}:${channelId ?? 'storage'}:${props.config.revision}`}
      channelId={channelId}
      disabled={props.disabled}
    />
  )
}

function AssetSecretDisclosureControl(props: {
  channelId?: number
  disabled?: boolean
}) {
  const { t } = useTranslation()
  const verification = useSecureVerification()
  const cancelVerification = verification.cancel
  const operation = useRef<AbortController | null>(null)
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
    if (props.disabled || operation.current) return
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
      <Dialog
        open={loading || secret !== null}
        onOpenChange={(open) => {
          if (open) void reveal()
          else close()
        }}
        trigger={
          <Button
            type='button'
            variant='outline'
            size='sm'
            disabled={props.disabled || verification.isActive || loading}
          >
            {t('Verify to view')}
          </Button>
        }
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
