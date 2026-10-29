<template>
  <BaseDialog
    :show="show"
    :title="t('admin.users.bulkBalance.title')"
    width="normal"
    @close="requestClose"
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
        <button
          type="button"
          class="btn btn-secondary"
          :disabled="submitting"
          data-test="cancel"
          @click="requestClose"
        >
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

<script lang="ts">
// Idempotency keys of batches that have not been answered for good, by batch
// content. Module-scoped so they outlive the dialog: the server keeps running a
// batch after the browser gives up (timeout, closed tab, proxy cut-off), and
// re-submitting the same batch under a fresh key would credit the users it had
// already finished a second time. One entry per batch, so trying a different
// batch in between does not cost the first one its key.
const pendingKeys = new Map<string, string>()

export function resetPendingBulkBalanceSubmission() {
  pendingKeys.clear()
}
</script>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api/admin'
import { useAppStore } from '@/stores/app'
import { extractApiErrorMessage } from '@/utils/apiError'
import BaseDialog from '@/components/common/BaseDialog.vue'

const props = defineProps<{
  show: boolean
  selectedIds: number[]
}>()

const emit = defineEmits<{
  close: []
  success: [affected: number, skippedIds: number[]]
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

// 同一批（用户、操作、金额、备注都相同）的重试复用同一个幂等键：请求在前端超时后，
// 后端仍会把这一批跑完，带着原键重试时后端回放那次结果而不是再加一遍。键放在模块
// 作用域（pendingKeys），关掉再打开对话框也不会丢；输入有任何变化就是另一批。
const newOperationKey = () =>
  `user-batch-balance-${globalThis.crypto?.randomUUID?.() ?? `${Date.now()}-${Math.random().toString(36).slice(2)}`}`

// 请求体里的 user_ids 与指纹用同一份去重排序后的列表。服务端按请求体算摘要，若请求
// 体保留勾选顺序，换个顺序选同一批人会命中同一把键、摘要却不同，重试永远 409。
const normalizedUserIDs = () => [...new Set(props.selectedIds)].sort((a, b) => a - b)

const submissionFingerprint = (ids: number[], amount: number) =>
  JSON.stringify({
    ids,
    operation: operation.value,
    amount,
    notes: notes.value.trim()
  })

// 只有服务端明确拒绝了这一次请求（与时机无关的 4xx），才说明它没有执行，可以作废这把键。
// 超时、断网、5xx、409（仍在处理）、429 都可能在服务端继续跑完，键要留着给重试。
const isDefinitiveRejection = (error: unknown) => {
  const status = (error as { status?: unknown } | null)?.status
  return (
    typeof status === 'number' &&
    status >= 400 &&
    status < 500 &&
    ![408, 409, 425, 429].includes(status)
  )
}

const reset = () => {
  operation.value = 'set'
  amountValue.value = ''
  notes.value = ''
}

watch(
  () => props.show,
  (show) => {
    // 请求仍在进行时不重置：提交中的状态必须保留，否则又能发出第二批。
    if (show && !submitting.value) reset()
  }
)

const requestClose = () => {
  if (submitting.value) return
  emit('close')
}

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
  const userIDs = normalizedUserIDs()
  const fingerprint = submissionFingerprint(userIDs, parsedAmount.value)
  let key = pendingKeys.get(fingerprint)
  if (!key) {
    key = newOperationKey()
    pendingKeys.set(fingerprint, key)
  }
  try {
    const result = await adminAPI.users.batchUpdateBalance(
      {
        user_ids: userIDs,
        balance: parsedAmount.value,
        operation: operation.value,
        notes: notes.value.trim()
      },
      key
    )
    pendingKeys.delete(fingerprint)
    const skippedItems = result.skipped ?? []
    if (skippedItems.length > 0) {
      appStore.showError(
        t('admin.users.bulkBalance.partialSuccessReselected', {
          count: result.affected,
          skipped: skippedItems.length,
          reason: skippedItems[0].reason
        })
      )
    } else {
      appStore.showSuccess(t('admin.users.bulkBalance.success', { count: result.affected }))
    }
    submitting.value = false
    emit(
      'success',
      result.affected,
      skippedItems.map((item) => item.id)
    )
    emit('close')
  } catch (error: unknown) {
    if (isDefinitiveRejection(error)) pendingKeys.delete(fingerprint)
    // apiClient 拒绝时给的是 { status, code, message }，没有 response 字段。
    appStore.showError(extractApiErrorMessage(error, t('admin.users.bulkBalance.failed')))
  } finally {
    submitting.value = false
  }
}
</script>
