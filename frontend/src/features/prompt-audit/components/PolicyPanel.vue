<template>
  <section aria-labelledby="prompt-policy-title" class="py-6">
    <div>
      <h2 id="prompt-policy-title" class="text-base font-semibold text-gray-950 dark:text-white">{{ t('admin.promptAudit.policy.title') }}</h2>
      <p class="mt-1 text-sm text-gray-500 dark:text-dark-300">{{ t('admin.promptAudit.policy.description') }}</p>
    </div>

    <div class="mt-5 grid gap-4 lg:grid-cols-[minmax(0,1fr)_minmax(260px,0.45fr)]">
      <div class="rounded-xl border border-gray-200 p-4 dark:border-dark-700/60 dark:bg-dark-900/20 sm:p-5">
        <fieldset>
          <legend class="text-sm font-medium text-gray-900 dark:text-white">{{ t('admin.promptAudit.policy.scope') }}</legend>
          <div class="mt-3 flex flex-wrap gap-5 text-sm text-gray-700 dark:text-dark-200">
            <label class="flex items-center gap-2">
              <input type="radio" name="prompt-audit-scope" :checked="draft.all_groups" @change="patch({ all_groups: true, group_ids: [] })" />
              {{ t('admin.promptAudit.policy.allGroups') }}
            </label>
            <label class="flex items-center gap-2">
              <input type="radio" name="prompt-audit-scope" :checked="!draft.all_groups" @change="patch({ all_groups: false })" />
              {{ t('admin.promptAudit.policy.selectedGroups') }}
            </label>
          </div>
        </fieldset>

        <div v-if="!draft.all_groups" class="mt-4">
          <label class="block text-sm text-gray-700 dark:text-dark-200">
            <span>{{ t('admin.promptAudit.policy.searchGroups') }}</span>
            <input v-model="groupSearch" type="search" class="input mt-1.5 w-full" :aria-label="t('admin.promptAudit.policy.searchGroups')" />
          </label>
          <div class="mt-3 max-h-52 overflow-y-auto rounded-lg border border-gray-200 p-2 dark:border-dark-700">
            <label v-for="group in filteredGroups" :key="group.id" class="flex cursor-pointer items-center justify-between gap-3 rounded-md px-2 py-2 text-sm hover:bg-gray-50 dark:hover:bg-dark-800">
              <span class="flex items-center gap-2 text-gray-800 dark:text-dark-100">
                <input type="checkbox" :checked="draft.group_ids.includes(group.id)" @change="toggleGroup(group.id)" />
                {{ group.name }}
              </span>
              <span class="text-xs text-gray-500 dark:text-dark-400">{{ group.platform }} · {{ group.status }}</span>
            </label>
            <p v-if="filteredGroups.length === 0" class="px-2 py-4 text-center text-sm text-gray-500">{{ t('admin.promptAudit.policy.noGroups') }}</p>
          </div>
          <div v-if="missingGroupIds.length" class="mt-3 rounded-lg bg-amber-50 px-3 py-2 text-sm text-amber-800 dark:bg-amber-950/30 dark:text-amber-200">
            {{ t('admin.promptAudit.policy.missingGroups') }}: {{ missingGroupIds.join(', ') }}
          </div>
          <p class="mt-2 text-xs text-gray-500 dark:text-dark-400">{{ t('admin.promptAudit.policy.selectedCount', { count: draft.group_ids.length }) }}</p>
        </div>

        <fieldset class="mt-5 border-t border-gray-100 pt-5 dark:border-dark-800">
          <legend class="text-sm font-medium text-gray-900 dark:text-white">{{ t('admin.promptAudit.policy.scanners') }}</legend>
          <p
            v-if="scannersInert"
            class="mt-2 rounded-lg bg-amber-50 px-3 py-2 text-xs text-amber-800 dark:bg-amber-950/30 dark:text-amber-200"
            data-test="scanners-inert-hint"
          >
            {{ t('admin.promptAudit.policy.scannersInert') }}
          </p>
          <div class="mt-3 grid gap-2 sm:grid-cols-2">
            <label v-for="scanner in SCANNER_CATALOG" :key="scanner.id" class="flex items-center gap-2 rounded-md px-2 py-1.5 text-sm text-gray-700 hover:bg-gray-50 dark:text-dark-200 dark:hover:bg-dark-800">
              <input type="checkbox" :checked="draft.scanners.includes(scanner.id)" :aria-label="scannerLabel(scanner.id)" @change="toggleScanner(scanner.id)" />
              <span>{{ scannerLabel(scanner.id) }}</span>
            </label>
          </div>
        </fieldset>
      </div>

      <div class="space-y-4 rounded-xl border border-gray-200 p-4 dark:border-dark-700/60 dark:bg-dark-900/20 sm:p-5">
        <label class="block text-sm text-gray-700 dark:text-dark-200">
          <span>{{ t('admin.promptAudit.policy.workerCount') }}</span>
          <input :value="draft.worker_count" type="number" min="1" max="32" class="input mt-1.5 w-full" :aria-label="t('admin.promptAudit.policy.workerCount')" @input="patch({ worker_count: Number(($event.target as HTMLInputElement).value) })" />
        </label>
        <label class="block text-sm text-gray-700 dark:text-dark-200">
          <span>{{ t('admin.promptAudit.policy.queueCapacity') }}</span>
          <input :value="draft.queue_capacity" type="number" min="1" max="100000" class="input mt-1.5 w-full" :aria-label="t('admin.promptAudit.policy.queueCapacity')" @input="patch({ queue_capacity: Number(($event.target as HTMLInputElement).value) })" />
        </label>
        <div class="rounded-lg bg-gray-50 px-4 py-3 text-sm text-gray-600 dark:bg-dark-900/50 dark:text-dark-300">
          <p class="font-medium text-gray-800 dark:text-dark-100">{{ t('admin.promptAudit.policy.strategy') }}</p>
          <p class="mt-1">priority · {{ t('admin.promptAudit.policy.strategyHint') }}</p>
        </div>
      </div>
    </div>

    <div class="mt-4 rounded-xl border border-gray-200 p-4 dark:border-dark-700/60 dark:bg-dark-900/20 sm:p-5">
      <div class="flex flex-wrap items-start justify-between gap-3">
        <div class="min-w-0">
          <h3 class="text-sm font-medium text-gray-900 dark:text-white">{{ t('admin.promptAudit.moderation.title') }}</h3>
          <p class="mt-1 text-sm text-gray-500 dark:text-dark-300">{{ t('admin.promptAudit.moderation.description') }}</p>
        </div>
        <div class="flex items-center gap-2">
          <button type="button" class="btn-secondary btn-sm" @click="emit('preview')">
            {{ t('admin.promptAudit.moderation.tryIt') }}
          </button>
          <button type="button" class="btn-secondary btn-sm" :disabled="isDefaultPrompt" @click="usePreset(defaultCustomPrompt)">
            {{ t('admin.promptAudit.moderation.restoreDefault') }}
          </button>
          <button
            v-if="categoryAwareCustomPrompt"
            type="button"
            class="btn-secondary btn-sm"
            :disabled="isCategoryAwarePrompt"
            data-test="use-category-preset"
            @click="usePreset(categoryAwareCustomPrompt)"
          >
            {{ t('admin.promptAudit.moderation.useCategoryPreset') }}
          </button>
        </div>
      </div>

      <p
        v-if="!usesCustomJSON"
        class="mt-3 rounded-lg bg-gray-50 px-3 py-2 text-xs text-gray-600 dark:bg-dark-900/50 dark:text-dark-300"
      >
        {{ t('admin.promptAudit.moderation.inactiveHint') }}
      </p>

      <label class="mt-3 block text-sm text-gray-700 dark:text-dark-200">
        <span class="flex flex-wrap items-center justify-between gap-2">
          <span>{{ t('admin.promptAudit.moderation.prompt') }}</span>
          <span class="text-xs tabular-nums" :class="promptTooLong ? 'text-red-600 dark:text-red-400' : 'text-gray-400'">
            {{ promptLength }} / {{ MAX_CUSTOM_PROMPT_RUNES }}
          </span>
        </span>
        <textarea
          :value="draft.custom_prompt"
          rows="14"
          spellcheck="false"
          class="input mt-1.5 w-full font-mono text-xs leading-relaxed"
          :aria-label="t('admin.promptAudit.moderation.prompt')"
          @input="patch({ custom_prompt: ($event.target as HTMLTextAreaElement).value })"
        />
      </label>
      <p class="mt-2 text-xs text-gray-500 dark:text-dark-400">{{ t('admin.promptAudit.moderation.promptHint') }}</p>
      <p class="mt-2 text-[10px] font-semibold uppercase tracking-wider text-gray-400">{{ t('admin.promptAudit.moderation.contractLabel') }}</p>
      <pre class="mt-1 overflow-x-auto rounded-lg bg-gray-50 p-3 font-mono text-[11px] text-gray-700 dark:bg-dark-900/60 dark:text-dark-200">{{ RESPONSE_CONTRACT_EXAMPLE }}</pre>
      <p v-if="promptTooLong" class="mt-1 text-xs text-red-600 dark:text-red-400">
        {{ t('admin.promptAudit.moderation.promptTooLong') }}
      </p>
      <p v-else-if="usesCustomJSON && !draft.custom_prompt.trim()" class="mt-1 text-xs text-red-600 dark:text-red-400">
        {{ t('admin.promptAudit.moderation.promptRequired') }}
      </p>

      <div class="mt-4 grid gap-4 sm:grid-cols-2">
        <label class="block text-sm text-gray-700 dark:text-dark-200">
          <span>{{ t('admin.promptAudit.moderation.blockThreshold') }}</span>
          <input
            :value="draft.block_threshold"
            type="number"
            min="0.01"
            max="1"
            step="0.01"
            class="input mt-1.5 w-full"
            :aria-label="t('admin.promptAudit.moderation.blockThreshold')"
            @input="patch({ block_threshold: Number(($event.target as HTMLInputElement).value) })"
          />
          <span class="mt-1 block text-xs text-gray-500 dark:text-dark-400">{{ t('admin.promptAudit.moderation.blockThresholdHint') }}</span>
        </label>
        <label class="block text-sm text-gray-700 dark:text-dark-200">
          <span>{{ t('admin.promptAudit.moderation.flagThreshold') }}</span>
          <input
            :value="draft.flag_threshold"
            type="number"
            min="0.01"
            max="1"
            step="0.01"
            class="input mt-1.5 w-full"
            :aria-label="t('admin.promptAudit.moderation.flagThreshold')"
            @input="patch({ flag_threshold: Number(($event.target as HTMLInputElement).value) })"
          />
          <span class="mt-1 block text-xs text-gray-500 dark:text-dark-400">{{ t('admin.promptAudit.moderation.flagThresholdHint') }}</span>
        </label>
      </div>
      <p v-if="thresholdsOutOfOrder" class="mt-2 text-xs text-red-600 dark:text-red-400">
        {{ t('admin.promptAudit.moderation.thresholdOrder') }}
      </p>
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import type { PromptAuditDraft, PromptAuditGroup } from '../types'
import { cloneData, hasEnabledCustomJSONEndpoint, SCANNER_CATALOG } from '../viewModel'

// Mirrors MaxCustomPromptRunes in backend/internal/securityaudit/prompt_types.go.
const MAX_CUSTOM_PROMPT_RUNES = 20000

// Kept out of i18n on purpose: vue-i18n parses `{` as a placeholder, so a raw
// JSON sample in a message string fails to compile.
const RESPONSE_CONTRACT_EXAMPLE =
  '{"risk":"safe|controversial|unsafe","confidence":0.00,"categories":[],"reason":"..."}'

const props = defineProps<{
  draft: PromptAuditDraft
  groups: PromptAuditGroup[]
  defaultCustomPrompt: string
  categoryAwareCustomPrompt?: string
}>()
const emit = defineEmits<{
  (event: 'update:draft', value: PromptAuditDraft): void
  (event: 'preview'): void
}>()
const { t } = useI18n()
const groupSearch = ref('')

const usesCustomJSON = computed(() => hasEnabledCustomJSONEndpoint(props.draft))
// Count code points, not UTF-16 units, so the limit matches the Go rune check.
const promptLength = computed(() => [...(props.draft.custom_prompt ?? '').trim()].length)
const promptTooLong = computed(() => promptLength.value > MAX_CUSTOM_PROMPT_RUNES)
const isDefaultPrompt = computed(
  () => (props.draft.custom_prompt ?? '').trim() === (props.defaultCustomPrompt ?? '').trim(),
)
const isCategoryAwarePrompt = computed(
  () =>
    Boolean(props.categoryAwareCustomPrompt) &&
    (props.draft.custom_prompt ?? '').trim() === (props.categoryAwareCustomPrompt ?? '').trim(),
)
// The default contract returns no categories, so the scanner toggles above only
// affect the verdict when the category-aware preset is in use.
const scannersInert = computed(() => usesCustomJSON.value && !isCategoryAwarePrompt.value)
const thresholdsOutOfOrder = computed(
  () => Number(props.draft.flag_threshold) > Number(props.draft.block_threshold),
)

function usePreset(prompt: string) {
  patch({ custom_prompt: prompt })
}

const filteredGroups = computed(() => {
  const query = groupSearch.value.trim().toLowerCase()
  if (!query) return props.groups
  return props.groups.filter((group) => `${group.name} ${group.id} ${group.platform}`.toLowerCase().includes(query))
})
const knownGroupIds = computed(() => new Set(props.groups.map((group) => group.id)))
const missingGroupIds = computed(() => props.draft.group_ids.filter((id) => !knownGroupIds.value.has(id)))

function patch(value: Partial<PromptAuditDraft>) {
  emit('update:draft', { ...cloneData(props.draft), ...value })
}
function toggleGroup(id: number) {
  const selected = new Set(props.draft.group_ids)
  if (selected.has(id)) selected.delete(id)
  else selected.add(id)
  patch({ group_ids: [...selected].sort((a, b) => a - b) })
}
function toggleScanner(id: string) {
  const selected = new Set(props.draft.scanners)
  if (selected.has(id)) selected.delete(id)
  else selected.add(id)
  patch({ scanners: SCANNER_CATALOG.map((item) => item.id).filter((item) => selected.has(item)) })
}
function scannerLabel(id: string): string {
  return t(`admin.promptAudit.scanners.${id}`)
}
</script>
