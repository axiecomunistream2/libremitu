<template>
  <div>
    <ReplyBox v-model:messageType="messageType" />
  </div>
</template>

<script setup>
import { ref } from 'vue'
import ReplyBox from './ReplyBox.vue'

const messageType = ref('reply')
</script>
