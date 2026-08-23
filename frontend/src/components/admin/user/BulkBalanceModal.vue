<template>
  <BaseDialog
    :show="show"
    :title="t('admin.users.bulkBalance.title')"
    width="normal"
    @close="emit('close')"
  >
    <form id="bulk-balance-form" class="space-y-5" @submit.prevent="handleSubmit">
      <p class="text-sm font-medium text-gray-700 dark:text-gray-300">
        {{ t('admin.users.bulkBalance.selectedCount', { count: selectedIds.length }) }}
      </p>

      <div>
        <label class="input-label" for="bulk-balance-operation">
          {{ t('admin.users.bulkBalance.operation') }}
        </label>
        <select
          id="bulk-balance-operation"
          v-model="operation"
          class="input"
          data-test="operation-select"
        >
          <option value="set">{{ t('admin.users.bulkBalance.operationSet') }}</option>
          <option value="add">{{ t('admin.users.bulkBalance.operationAdd') }}</option>
          <option value="subtract">{{ t('admin.users.bulkBalance.operationSubtract') }}</option>
        </select>
        <p class="input-hint">
          {{ operationHint }}
        </p>
      </div>

      <div>
        <label class="input-label" for="bulk-balance-amount">
          {{ t('admin.users.bulkBalance.amount') }}
        </label>
        <input
          id="bulk-balance-amount"
          v-model="amountValue"
          type="number"
          :min="operation === 'set' ? 0 : 0.01"
          step="0.01"
          class="input"
          data-test="amount-input"
        />
      </div>

      <div>
        <label class="input-label" for="bulk-balance-notes">
          {{ t('admin.users.bulkBalance.notes') }}
        </label>
        <input
          id="bulk-balance-notes"
          v-model="notes"
          type="text"
          class="input"
          maxlength="200"
          :placeholder="t('admin.users.bulkBalance.notesPlaceholder')"
          data-test="notes-input"
        />
      </div>

      <p v-if="hasInvalidValue" class="text-sm text-red-600 dark:text-red-400">
        {{
          operation === 'set'
            ? t('admin.users.bulkBalance.invalidSetAmount')
            : t('admin.users.bulkBalance.invalidDeltaAmount')
        }}
      </p>
      <p v-if="selectionTooLarge" class="text-sm text-red-600 dark:text-red-400">
        {{ t('admin.users.bulkLimits.selectionLimit', { max: MAX_BATCH_USER_IDS }) }}
      </p>
    </form>

    <template #footer>
      <div class="flex justify-end gap-3">
        <button type="button" class="btn btn-secondary" @click="emit('close')">
          {{ t('common.cancel') }}
        </button>
        <button
          type="submit"
          form="bulk-balance-form"
          class="btn btn-primary"
          :disabled="!canSubmit"
          data-test="submit"
        >
          {{ submitting ? t('admin.users.bulkBalance.applying') : t('admin.users.bulkBalance.apply') }}
        </button>
      </div>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api/admin'
import { useAppStore } from '@/stores/app'
import BaseDialog from '@/components/common/BaseDialog.vue'

const props = defineProps<{
  show: boolean
  selectedIds: number[]
}>()

const emit = defineEmits<{
  close: []
  success: [affected: number]
}>()

const { t } = useI18n()
const appStore = useAppStore()
const operation = ref<'set' | 'add' | 'subtract'>('set')
const amountValue = ref<string | number>('')
const notes = ref('')
const submitting = ref(false)
const MAX_BATCH_USER_IDS = 500

const parsedAmount = computed<number | null>(() => {
  const trimmed = String(amountValue.value).trim()
  if (!trimmed) return null
  const parsed = Number(trimmed)
  if (!Number.isFinite(parsed)) return null
  // set 允许 0（清零），add/subtract 必须 > 0
  if (operation.value === 'set' ? parsed < 0 : parsed <= 0) return null
  return parsed
})

const hasInvalidValue = computed(
  () => String(amountValue.value).trim() !== '' && parsedAmount.value === null
)
const selectionTooLarge = computed(() => props.selectedIds.length > MAX_BATCH_USER_IDS)
const canSubmit = computed(
  () =>
    props.selectedIds.length > 0 &&
    !selectionTooLarge.value &&
    parsedAmount.value !== null &&
    !submitting.value
)

const operationHint = computed(() => {
  switch (operation.value) {
    case 'set':
      return t('admin.users.bulkBalance.operationSetHint')
    case 'add':
      return t('admin.users.bulkBalance.operationAddHint')
    default:
      return t('admin.users.bulkBalance.operationSubtractHint')
  }
})

const reset = () => {
  operation.value = 'set'
  amountValue.value = ''
  notes.value = ''
  submitting.value = false
}

watch(
  () => props.show,
  (show) => {
    if (show) reset()
  }
)

const handleSubmit = async () => {
  if (!canSubmit.value || parsedAmount.value === null) return

  const operationLabel =
    operation.value === 'set'
      ? t('admin.users.bulkBalance.operationSet')
      : operation.value === 'add'
        ? t('admin.users.bulkBalance.operationAdd')
        : t('admin.users.bulkBalance.operationSubtract')
  const confirmed = window.confirm(
    t('admin.users.bulkBalance.confirm', {
      count: props.selectedIds.length,
      operation: operationLabel,
      amount: parsedAmount.value
    })
  )
  if (!confirmed) return

  submitting.value = true
  try {
    const result = await adminAPI.users.batchUpdateBalance({
      user_ids: [...props.selectedIds],
      balance: parsedAmount.value,
      operation: operation.value,
      notes: notes.value.trim()
    })
    const skipped = result.skipped?.length ?? 0
    if (skipped > 0) {
      appStore.showError(
        t('admin.users.bulkBalance.partialSuccess', { count: result.affected, skipped })
      )
    } else {
      appStore.showSuccess(t('admin.users.bulkBalance.success', { count: result.affected }))
    }
    emit('success', result.affected)
    emit('close')
  } catch (error: any) {
    appStore.showError(
      error.response?.data?.message ||
        error.response?.data?.detail ||
        t('admin.users.bulkBalance.failed')
    )
  } finally {
    submitting.value = false
  }
}
</script>
