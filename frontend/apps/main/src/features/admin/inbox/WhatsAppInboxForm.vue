<template>
  <form @submit="onSubmit" class="space-y-6 w-full">
    <FormField v-slot="{ componentField }" name="name">
      <FormItem>
        <FormLabel>{{ $t('globals.terms.name') }}</FormLabel>
        <FormControl>
          <Input type="text" placeholder="" v-bind="componentField" />
        </FormControl>
        <FormMessage />
      </FormItem>
    </FormField>

    <FormField v-slot="{ componentField, handleChange }" name="enabled">
      <FormItem>
        <SwitchField
          :title="$t('globals.terms.enabled')"
          :checked="componentField.modelValue"
          @update:checked="handleChange"
        />
      </FormItem>
    </FormField>

    <FormField v-slot="{ componentField, handleChange }" name="prompt_tags_on_reply">
      <FormItem>
        <SwitchField
          :title="$t('admin.inbox.promptTagsOnReply')"
          :description="$t('admin.inbox.promptTagsOnReply.description')"
          :checked="componentField.modelValue"
          @update:checked="handleChange"
        />
      </FormItem>
    </FormField>

    <FormField v-slot="{ componentField, handleChange }" name="csat_enabled">
      <FormItem>
        <SwitchField
          :title="$t('admin.inbox.csatSurveys')"
          :description="$t('admin.inbox.csatSurveys.description_1')"
          :checked="componentField.modelValue"
          @update:checked="handleChange"
        />
      </FormItem>
    </FormField>

    <div class="max-w-xl">
      <FormField v-slot="{ componentField }" name="reopen_window_hours">
        <FormItem>
          <FormLabel>{{ $t('admin.inbox.whatsapp.reopenWindow') }}</FormLabel>
          <FormControl>
            <Input type="number" min="0" placeholder="48" v-bind="componentField" />
          </FormControl>
          <FormDescription>
            {{ $t('admin.inbox.whatsapp.reopenWindow.description') }}
          </FormDescription>
          <FormMessage />
        </FormItem>
      </FormField>
    </div>

    <div v-show="csatEnabled" class="box p-4 space-y-4">
      <div>
        <h3 class="ui-heading">{{ $t('globals.terms.csatSurveyTemplate') }}</h3>
      </div>
      <FormField v-slot="{ componentField }" name="config.csat_message">
        <FormItem>
          <FormLabel>{{ $t('admin.inbox.whatsapp.csatMessage') }}</FormLabel>
          <FormControl>
            <Textarea rows="3" v-bind="componentField" />
          </FormControl>
          <FormDescription>
            {{ $t('admin.inbox.whatsapp.csatMessage.description') }}
          </FormDescription>
          <FormMessage />
        </FormItem>
      </FormField>
    </div>

    <WagoDevicePanel />

    <div v-if="webhookURL" class="box p-4 space-y-2">
      <h3 class="ui-heading">{{ $t('globals.terms.webhook', 1) }}</h3>
      <p class="ui-description">{{ $t('admin.inbox.whatsapp.webhook.description') }}</p>
      <div class="flex items-center gap-2 max-w-xl">
        <Input :model-value="webhookURL" readonly class="font-mono text-xs" />
        <CopyButton :text="webhookURL" />
      </div>
    </div>

    <Button type="submit" :is-loading="isLoading" :disabled="isLoading">
      {{ submitLabel }}
    </Button>
  </form>
</template>

<script setup>
import { computed, watch } from 'vue'
import { useForm } from 'vee-validate'
import { toTypedSchema } from '@vee-validate/zod'
import { createFormSchema } from './whatsappFormSchema.js'
import {
  FormControl,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
  FormDescription
} from '@shared-ui/components/ui/form/index.js'
import { Input } from '@shared-ui/components/ui/input/index.js'
import { Textarea } from '@shared-ui/components/ui/textarea/index.js'
import SwitchField from '@shared-ui/components/SwitchField.vue'
import { Button } from '@shared-ui/components/ui/button/index.js'
import CopyButton from '@/components/button/CopyButton.vue'
import WagoDevicePanel from './WagoDevicePanel.vue'
import { useI18n } from 'vue-i18n'

const props = defineProps({
  initialValues: {
    type: Object,
    default: () => ({})
  },
  submitForm: {
    type: Function,
    required: true
  },
  submitLabel: {
    type: String,
    default: ''
  },
  isNewForm: {
    type: Boolean,
    default: false
  },
  isLoading: {
    type: Boolean,
    default: false
  }
})

const { t } = useI18n()
const webhookURL = computed(() => props.initialValues?.webhook_url || '')
const submitLabel = computed(
  () => props.submitLabel || (props.isNewForm ? t('globals.messages.create') : t('globals.messages.save'))
)

const form = useForm({
  validationSchema: computed(() => toTypedSchema(createFormSchema(t))),
  initialValues: {
    name: '',
    enabled: true,
    csat_enabled: false,
    prompt_tags_on_reply: false,
    reopen_window_hours: 48,
    config: {
      csat_message: ''
    }
  }
})

const csatEnabled = computed(() => form.values.csat_enabled)

const onSubmit = form.handleSubmit(async (values) => {
  await props.submitForm(values)
})

watch(
  () => props.initialValues,
  (newValues) => {
    if (Object.keys(newValues).length === 0) {
      return
    }
    // An unset reopen window is off (0), not the new-inbox default of 48.
    form.setValues(
      {
        ...newValues,
        reopen_window_hours: newValues.reopen_window_hours ?? 0,
        config: {
          ...(newValues.config || {}),
          csat_message: newValues.config?.csat_message || ''
        }
      },
      false
    )
  },
  { deep: true, immediate: true }
)
</script>
