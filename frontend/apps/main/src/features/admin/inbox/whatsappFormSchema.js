import * as z from 'zod'

export const createFormSchema = (t) =>
  z.object({
    name: z.string().min(1, t('globals.messages.required')),
    enabled: z.boolean().optional(),
    csat_enabled: z.boolean().optional(),
    prompt_tags_on_reply: z.boolean().optional(),
    reopen_window_hours: z.coerce.number().int().min(0).optional(),
    config: z.object({
      csat_message: z.string().optional()
    })
  })
