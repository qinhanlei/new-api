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
import { Eye, EyeOff } from 'lucide-react'
import * as React from 'react'
import { useTranslation } from 'react-i18next'

import { cn } from '@/lib/utils'

import { Button } from './ui/button'
import { Input } from './ui/input'

type PasswordInputProps = Omit<
  React.InputHTMLAttributes<HTMLInputElement>,
  'type'
> & {
  ref?: React.Ref<HTMLInputElement>
  visibilityAction?: {
    label: string
    onClick: React.MouseEventHandler<HTMLButtonElement>
    disabled?: boolean
  }
}

export function PasswordInput({
  className,
  disabled,
  ref,
  visibilityAction,
  ...props
}: PasswordInputProps) {
  const { t } = useTranslation()
  const [showPassword, setShowPassword] = React.useState(false)
  const visible = showPassword && !visibilityAction
  const visibilityLabel =
    visibilityAction?.label ?? (visible ? t('Hide input') : t('Show input'))

  return (
    <div className={cn('relative rounded-md', className)}>
      <Input
        type={visible ? 'text' : 'password'}
        ref={ref}
        disabled={disabled}
        className='pe-9'
        {...props}
        onChange={(event) => {
          if (!event.currentTarget.value) setShowPassword(false)
          props.onChange?.(event)
        }}
      />
      <Button
        type='button'
        size='icon'
        variant='ghost'
        disabled={disabled || visibilityAction?.disabled}
        className='text-muted-foreground absolute end-1 top-1/2 h-6 w-6 -translate-y-1/2 rounded-md'
        onClick={
          visibilityAction?.onClick ?? (() => setShowPassword((prev) => !prev))
        }
        aria-label={visibilityLabel}
        title={visibilityLabel}
      >
        {visible ? (
          <Eye size={18} aria-hidden='true' />
        ) : (
          <EyeOff size={18} aria-hidden='true' />
        )}
      </Button>
    </div>
  )
}
