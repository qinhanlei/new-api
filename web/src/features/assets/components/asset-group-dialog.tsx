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
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useId } from 'react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Dialog } from '@/components/dialog'
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
import { handleServerError } from '@/lib/handle-server-error'

import { assetQueryKeys, executeAssetAction } from '../api'
import { assetGroupSchema } from '../lib/schemas'
import type { AssetGroup, AssetScope } from '../types'

export function AssetGroupDialog({
  group,
  project,
  scope,
  onClose,
}: {
  group?: AssetGroup
  project: string
  scope: AssetScope
  onClose: () => void
}) {
  const { t } = useTranslation()
  const id = useId()
  const client = useQueryClient()
  const form = useForm<{ name: string }>({
    resolver: zodResolver(assetGroupSchema(t)),
    defaultValues: { name: group?.Name ?? '' },
  })
  const mutation = useMutation({
    mutationFn: ({ name }: { name: string }) =>
      executeAssetAction(
        group ? 'UpdateAssetGroup' : 'CreateAssetGroup',
        {
          ProjectName: project,
          ...(group ? { Id: group.Id } : { GroupType: 'AIGC' }),
          Name: name,
        },
        scope
      ),
    onSuccess: () => {
      void client.invalidateQueries({ queryKey: assetQueryKeys.all })
      toast.success(t('Saved successfully'))
      onClose()
    },
    onError: (error) => handleServerError(error),
  })
  return (
    <Dialog
      open
      onOpenChange={(open) => !open && !mutation.isPending && onClose()}
      title={group ? t('Rename asset group') : t('Create asset group')}
      contentHeight='auto'
      footer={
        <>
          <Button
            variant='outline'
            onClick={onClose}
            disabled={mutation.isPending}
          >
            {t('Cancel')}
          </Button>
          <Button type='submit' form={id} disabled={mutation.isPending}>
            {mutation.isPending ? t('Saving...') : t('Save')}
          </Button>
        </>
      }
    >
      <Form {...form}>
        <form
          id={id}
          onSubmit={form.handleSubmit((values) => mutation.mutate(values))}
        >
          <FormField
            control={form.control}
            name='name'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Name')}</FormLabel>
                <FormControl>
                  <Input {...field} autoFocus maxLength={64} />
                </FormControl>
                <FormMessage />
              </FormItem>
            )}
          />
        </form>
      </Form>
    </Dialog>
  )
}
