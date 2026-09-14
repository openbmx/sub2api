<template>
  <div class="space-y-2">
    <div class="flex items-center justify-between gap-3">
      <label class="input-label mb-0">{{ t('admin.accounts.opencodeModelProtocols.title') }}</label>
      <button
        type="button"
        class="btn btn-secondary"
        data-test="add-opencode-model-protocol"
        :disabled="rows.length >= MAX_OPENCODE_MODEL_PROTOCOLS"
        @click="addRow"
      >
        {{ t('admin.accounts.opencodeModelProtocols.add') }}
      </button>
    </div>

    <div v-for="(row, index) in rows" :key="index" class="flex items-center gap-2">
      <input
        v-model="row.prefix"
        class="input flex-1"
        :placeholder="t('admin.accounts.opencodeModelProtocols.prefixPlaceholder')"
        :aria-label="t('admin.accounts.opencodeModelProtocols.prefix')"
        @input="emitChange"
      />
      <select
        v-model="row.protocol"
        class="input flex-1"
        :aria-label="t('admin.accounts.opencodeModelProtocols.protocol')"
        @change="emitChange"
      >
        <option v-for="protocol in OPENCODE_NATIVE_PROTOCOLS" :key="protocol" :value="protocol">
          {{ t(`admin.accounts.cnProviders.apiProtocol.${protocolLabelKey(protocol)}`) }}
        </option>
      </select>
      <button
        type="button"
        class="btn btn-secondary"
        :aria-label="t('admin.accounts.opencodeModelProtocols.remove')"
        @click="removeRow(index)"
      >
        {{ t('common.delete') }}
      </button>
    </div>

    <p class="text-xs text-gray-500 dark:text-dark-400">
      {{ t('admin.accounts.opencodeModelProtocols.hint') }}
    </p>
  </div>
</template>

<script setup lang="ts">
import { ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  MAX_OPENCODE_MODEL_PROTOCOLS,
  OPENCODE_NATIVE_PROTOCOLS,
  type CnNativeApiProtocol,
  type OpenCodeModelProtocolRow
} from './credentialsBuilder'

const props = defineProps<{ modelValue: OpenCodeModelProtocolRow[] }>()
const emit = defineEmits<{ (event: 'update:modelValue', value: OpenCodeModelProtocolRow[]): void }>()
const { t } = useI18n()

// A record has no stable order and cannot hold a half-typed prefix, so the
// editor owns rows and folds them back on every change.
const rows = ref<OpenCodeModelProtocolRow[]>(props.modelValue.map((row) => ({ ...row })))

watch(
  () => props.modelValue,
  (next) => {
    // Only resync when the parent genuinely replaced the list (account switch),
    // otherwise typing would be clobbered by the round trip.
    if (JSON.stringify(next) === JSON.stringify(rows.value)) return
    rows.value = next.map((row) => ({ ...row }))
  }
)

function emitChange() {
  emit('update:modelValue', rows.value.map((row) => ({ ...row })))
}
function addRow() {
  if (rows.value.length >= MAX_OPENCODE_MODEL_PROTOCOLS) return
  rows.value.push({ prefix: '', protocol: 'chat_completions' })
  emitChange()
}
function removeRow(index: number) {
  rows.value.splice(index, 1)
  emitChange()
}
/** 复用 cnProviders.apiProtocol 下已有的协议名文案，避免同名两套翻译。 */
function protocolLabelKey(protocol: CnNativeApiProtocol): string {
  return protocol === 'chat_completions' ? 'chatCompletions' : protocol
}
</script>
