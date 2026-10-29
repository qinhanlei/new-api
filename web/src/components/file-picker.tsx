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
import { File as FileIcon, FolderOpen, Upload, X } from 'lucide-react'
import { useId, useRef, useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import { cn } from '@/lib/utils'

interface FilePickerProps {
  fileName: string
  label: string
  prompt: string
  accept: string
  description?: ReactNode
  selectedIcon?: ReactNode
  error?: string
  disabled?: boolean
  onSelect: (files: File[]) => void
  onRemove?: () => void
}

/** Selection and drop share one path; callers validate before replacing a draft. */
export function FilePicker(props: FilePickerProps): ReactNode {
  const { t } = useTranslation()
  const id = useId()
  const inputRef = useRef<HTMLInputElement>(null)
  const [isDragActive, setIsDragActive] = useState(false)

  return (
    <div
      data-slot='file-picker'
      className={cn(
        'flex min-w-0 flex-col items-center gap-2 rounded-lg border border-dashed px-4 py-6 text-center transition-colors',
        isDragActive && !props.disabled
          ? 'border-primary bg-primary/5'
          : 'bg-muted/20'
      )}
      onDragOver={(event) => {
        event.preventDefault()
        if (!props.disabled) setIsDragActive(true)
      }}
      onDragLeave={(event) => {
        if (!event.currentTarget.contains(event.relatedTarget as Node | null)) {
          setIsDragActive(false)
        }
      }}
      onDrop={(event) => {
        event.preventDefault()
        setIsDragActive(false)
        if (!props.disabled && event.dataTransfer.files.length) {
          props.onSelect([...event.dataTransfer.files])
        }
      }}
    >
      {props.fileName ? (
        (props.selectedIcon ?? (
          <FileIcon
            aria-hidden='true'
            className='text-muted-foreground size-5'
          />
        ))
      ) : (
        <Upload aria-hidden='true' className='text-muted-foreground size-5' />
      )}
      <div className='max-w-full min-w-0 space-y-0.5'>
        <p className='text-sm font-medium wrap-anywhere'>
          {props.fileName || props.prompt}
        </p>
        {props.description && (
          <div
            id={`${id}-description`}
            className='text-muted-foreground text-xs'
          >
            {props.description}
          </div>
        )}
      </div>
      <div className='flex flex-wrap justify-center gap-2'>
        <Button
          type='button'
          variant='outline'
          size='sm'
          disabled={props.disabled}
          onClick={() => inputRef.current?.click()}
        >
          <FolderOpen aria-hidden='true' />
          {props.fileName ? t('Choose another file') : t('Choose file')}
        </Button>
        {props.fileName && props.onRemove && (
          <Button
            type='button'
            variant='ghost'
            size='sm'
            disabled={props.disabled}
            onClick={props.onRemove}
          >
            <X aria-hidden='true' />
            {t('Remove file')}
          </Button>
        )}
      </div>
      <label className='sr-only' htmlFor={id}>
        {props.label}
      </label>
      <input
        ref={inputRef}
        id={id}
        type='file'
        accept={props.accept}
        className='sr-only'
        disabled={props.disabled}
        aria-invalid={Boolean(props.error)}
        aria-describedby={
          [
            props.description && `${id}-description`,
            props.error && `${id}-error`,
          ]
            .filter(Boolean)
            .join(' ') || undefined
        }
        onChange={(event) => {
          if (!props.disabled && event.target.files?.length) {
            props.onSelect([...event.target.files])
          }
          // Reset even after rejection so the same file can be picked again.
          event.target.value = ''
        }}
      />
      {props.error && (
        <p id={`${id}-error`} role='alert' className='text-destructive text-sm'>
          {props.error}
        </p>
      )}
    </div>
  )
}
