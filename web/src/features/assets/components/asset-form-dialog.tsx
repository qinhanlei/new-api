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
import { useForm, useWatch } from 'react-hook-form'
import { useTranslation } from 'react-i18next'

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
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs'

import { renameAsset, uploadAsset } from '../api'
import { assetFormSchema, type AssetFormValues } from '../lib/validation'
import type { Asset } from '../types'

export function AssetFormDialog(props: { asset?: Asset; onClose: () => void }) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const form = useForm<AssetFormValues>({
    resolver: zodResolver(assetFormSchema),
    defaultValues: {
      name: props.asset?.name ?? '',
      source: props.asset ? 'rename' : 'file',
      url: '',
    },
  })
  const source = useWatch({ control: form.control, name: 'source' })
  const mutation = useMutation({
    mutationFn: (value: AssetFormValues) =>
      props.asset
        ? renameAsset(props.asset.id, value.name)
        : uploadAsset({
            name: value.name,
            file: value.source === 'file' ? value.file : undefined,
            url: value.source === 'url' ? value.url : undefined,
          }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ['assets'] })
      props.onClose()
    },
  })
  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open && !mutation.isPending) props.onClose()
      }}
      title={props.asset ? t('Rename asset') : t('Upload asset')}
      description={
        props.asset
          ? undefined
          : t('Images, MP4 video, or MP3/WAV/AIFF audio, up to 100 MiB.')
      }
      footer={
        <>
          <Button
            variant='outline'
            disabled={mutation.isPending}
            onClick={props.onClose}
          >
            {t('Cancel')}
          </Button>
          <Button type='submit' form='asset-form' disabled={mutation.isPending}>
            {mutation.isPending ? t('Saving...') : t('Save')}
          </Button>
        </>
      }
    >
      <Form {...form}>
        <form
          id='asset-form'
          className='space-y-4'
          onSubmit={form.handleSubmit((value) => mutation.mutate(value))}
        >
          {!props.asset && (
            <Tabs
              value={source}
              onValueChange={(value) => {
                if (value !== 'file' && value !== 'url') return
                form.resetField('file')
                form.setValue('source', value)
              }}
            >
              <TabsList>
                <TabsTrigger value='file' disabled={mutation.isPending}>
                  {t('File')}
                </TabsTrigger>
                <TabsTrigger value='url' disabled={mutation.isPending}>
                  {t('Import URL')}
                </TabsTrigger>
              </TabsList>
            </Tabs>
          )}
          {source === 'file' && (
            <FormField
              control={form.control}
              name='file'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t('File')}</FormLabel>
                  <FormControl>
                    <Input
                      type='file'
                      accept='.jpg,.jpeg,.png,.gif,.webp,.mp4,.mp3,.wav,.aiff,.aif'
                      disabled={mutation.isPending}
                      onBlur={field.onBlur}
                      ref={field.ref}
                      name={field.name}
                      onChange={(event) => {
                        const file = event.target.files?.[0]
                        field.onChange(file)
                        if (file && !form.getValues('name')) {
                          form.setValue('name', file.name)
                        }
                      }}
                    />
                  </FormControl>
                  <FormMessage />
                </FormItem>
              )}
            />
          )}
          {source === 'url' && (
            <FormField
              control={form.control}
              name='url'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t('Source URL')}</FormLabel>
                  <FormControl>
                    <Input
                      {...field}
                      type='url'
                      placeholder='https://example.com/image.png'
                      disabled={mutation.isPending}
                    />
                  </FormControl>
                  <FormMessage />
                </FormItem>
              )}
            />
          )}
          <FormField
            control={form.control}
            name='name'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Name')}</FormLabel>
                <FormControl>
                  <Input {...field} disabled={mutation.isPending} />
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
