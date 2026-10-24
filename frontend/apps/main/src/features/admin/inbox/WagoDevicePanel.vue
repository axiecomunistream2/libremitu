<template>
  <div class="box p-4 space-y-4">
    <div class="flex items-start justify-between gap-3">
      <div>
        <h3 class="ui-heading">{{ $t('admin.inbox.whatsapp.device.title') }}</h3>
        <p class="ui-description">{{ $t('admin.inbox.whatsapp.device.description') }}</p>
      </div>
      <Button variant="outline" size="sm" :is-loading="loading" @click="load">
        {{ $t('globals.messages.refresh') }}
      </Button>
    </div>

    <p v-if="error" class="text-sm text-destructive">{{ error }}</p>

    <div v-else class="grid gap-2 text-sm sm:grid-cols-2">
      <div>
        <span class="text-muted-foreground">{{ $t('globals.terms.device') }}: </span>
        <span class="font-mono text-xs">{{ state.id || '-' }}</span>
      </div>
      <div>
        <span class="text-muted-foreground">{{ $t('globals.terms.status') }}: </span>
        <span :class="state.is_connected ? 'text-emerald-600' : 'text-destructive'">
          {{
            state.is_connected
              ? $t('admin.inbox.whatsapp.device.connected')
              : $t('admin.inbox.whatsapp.device.disconnected')
          }}
        </span>
        <span class="text-muted-foreground"> · </span>
        <span :class="state.is_logged_in ? 'text-emerald-600' : 'text-destructive'">
          {{
            state.is_logged_in
              ? $t('admin.inbox.whatsapp.device.loggedIn')
              : $t('admin.inbox.whatsapp.device.loggedOut')
          }}
        </span>
      </div>
      <div v-if="state.phone_number">
        <span class="text-muted-foreground">{{ $t('globals.terms.phoneNumber') }}: </span>
        {{ state.phone_number }}
      </div>
      <div v-if="state.display_name">
        <span class="text-muted-foreground">{{ $t('globals.terms.name', 1) }}: </span>
        {{ state.display_name }}
      </div>
    </div>

    <div class="flex flex-wrap gap-2">
      <Button v-if="!state.is_logged_in" size="sm" :is-loading="actionLoading" @click="startLogin">
        {{ $t('admin.inbox.whatsapp.device.login') }}
      </Button>
      <Button
        variant="outline"
        size="sm"
        :disabled="!state.is_logged_in || actionLoading"
        @click="reconnect"
      >
        {{ $t('admin.inbox.whatsapp.device.reconnect') }}
      </Button>
      <Button
        variant="destructive"
        size="sm"
        :disabled="!state.is_logged_in || actionLoading"
        @click="logout"
      >
        {{ $t('admin.inbox.whatsapp.device.logout') }}
      </Button>
    </div>

    <Dialog v-model:open="qrOpen">
      <DialogContent class="sm:max-w-sm">
        <DialogHeader>
          <DialogTitle>{{ $t('admin.inbox.whatsapp.device.qrTitle') }}</DialogTitle>
        </DialogHeader>
        <div class="flex flex-col items-center gap-3">
          <img v-if="qrDataURL" :src="qrDataURL" alt="WhatsApp QR" class="size-64 rounded border" />
          <p class="text-sm text-muted-foreground text-center">
            {{ $t('admin.inbox.whatsapp.device.qrExpires') }}
          </p>
        </div>
      </DialogContent>
    </Dialog>
  </div>
</template>

<script setup>
import { onBeforeUnmount, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { Button } from '@shared-ui/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle
} from '@shared-ui/components/ui/dialog'
import { EMITTER_EVENTS } from '@main/constants/emitterEvents.js'
import { useEmitter } from '@main/composables/useEmitter'
import { handleHTTPError } from '@shared-ui/utils/http.js'
import api from '@main/api'

const { t } = useI18n()
const emitter = useEmitter()
const state = ref({})
const loading = ref(false)
const actionLoading = ref(false)
const error = ref('')
const qrOpen = ref(false)
const qrDataURL = ref('')
let pollTimer = null

const load = async () => {
  loading.value = true
  error.value = ''
  try {
    const resp = await api.getWagoDevice()
    state.value = resp.data.data || {}
  } catch (err) {
    error.value = handleHTTPError(err).message || t('admin.inbox.whatsapp.device.loadError')
  } finally {
    loading.value = false
  }
}

const stopPolling = () => {
  if (pollTimer) {
    clearInterval(pollTimer)
    pollTimer = null
  }
}

const pollStatus = () => {
  stopPolling()
  pollTimer = setInterval(async () => {
    try {
      const resp = await api.getWagoDevice()
      state.value = resp.data.data || {}
      if (state.value.is_logged_in) {
        qrOpen.value = false
        stopPolling()
      }
    } catch {
      // Keep the QR visible and let the next tick retry.
    }
  }, 3000)
}

const startLogin = async () => {
  actionLoading.value = true
  try {
    const resp = await api.wagoLogin()
    qrDataURL.value = resp.data.data?.qr_data_url || ''
    qrOpen.value = true
    pollStatus()
  } catch (err) {
    emitter.emit(EMITTER_EVENTS.SHOW_TOAST, {
      variant: 'destructive',
      description: handleHTTPError(err).message
    })
  } finally {
    actionLoading.value = false
  }
}

const reconnect = async () => {
  actionLoading.value = true
  try {
    await api.wagoReconnect()
    await load()
    emitter.emit(EMITTER_EVENTS.SHOW_TOAST, {
      description: t('globals.messages.savedSuccessfully')
    })
  } catch (err) {
    emitter.emit(EMITTER_EVENTS.SHOW_TOAST, {
      variant: 'destructive',
      description: handleHTTPError(err).message
    })
  } finally {
    actionLoading.value = false
  }
}

const logout = async () => {
  if (!window.confirm(t('admin.inbox.whatsapp.device.logoutConfirm'))) return
  actionLoading.value = true
  try {
    await api.wagoLogout()
    await load()
  } catch (err) {
    emitter.emit(EMITTER_EVENTS.SHOW_TOAST, {
      variant: 'destructive',
      description: handleHTTPError(err).message
    })
  } finally {
    actionLoading.value = false
  }
}

onMounted(load)
onBeforeUnmount(stopPolling)
</script>
