<script setup lang="ts">
import {
	Alert,
	Button,
	Dialog,
	dayjs,
	LoadingText,
	TextInput,
	Tooltip,
	useCall,
} from 'frappe-ui'
import { computed, ref, watch } from 'vue'
import { API, method } from '@/api/methods'
import { useBillingOverview } from '@/composables/useBillingOverview'
import { useCapabilities } from '@/composables/useCapabilities'
import { useSession } from '@/composables/useSession'
import { useSpendingLimits } from '@/composables/useSpendingLimits'
import { whenTeamReady } from '@/composables/useTeamScope'
import { getErrorMessage, successToast } from '@/lib/feedback'
import { currencySymbol, money } from '@/lib/format'
import type { BillingSettings } from '@/types/billing'

defineProps<{ active?: boolean }>()
defineEmits<{ open: []; openTiers: [] }>()

const { forecast, currency } = useBillingOverview()
const { activeTeam } = useSession()
const { canManageBilling } = useCapabilities()
const { current: tierCurrent } = useSpendingLimits()

const loading = computed(() => forecast.loading && !forecast.data)
const fc = computed(() => forecast.data)
const projected = computed(() => Number(fc.value?.projected_total ?? 0))
const soFar = computed(() => Number(fc.value?.measured ?? projected.value))
const hasCycle = computed(() => projected.value > 0)
const daysRemaining = computed(() => fc.value?.days_remaining ?? null)

const cycleLabel = computed(() => {
	if (!fc.value?.period_start || !fc.value.period_end) return 'This cycle'

	const start = dayjs(fc.value.period_start)

	return `${start.format('MMMM')} cycle · ${start.format('D')} – ${dayjs(fc.value.period_end).format('D MMM')}`
})

const limit = computed(() => Number(tierCurrent.value?.max_spend ?? 0))
const scale = computed(() => Math.max(soFar.value, limit.value))

const percentOf = (amount: number): string =>
	`${scale.value ? (amount / scale.value) * 100 : 0}%`

const withinLimit = computed(() =>
	limit.value ? Math.min(soFar.value, limit.value) : soFar.value,
)

const overLimit = computed(() =>
	limit.value ? Math.max(0, soFar.value - limit.value) : 0,
)

// ── Billing alert (spend-alert threshold) ────────────────────────────────────
// Notify the team once projected spend crosses this amount (0 = off). Stored on
// the Billing Profile via get/save_billing_settings.
const settings = useCall<BillingSettings, { team: string }>({
	url: method(API.billingSettings),
	params: () => ({ team: activeTeam.value! }),
	immediate: false,
	refetch: true,
})

const saveAlert = useCall<
	unknown,
	{ team: string; spend_alert_threshold: number }
>({
	url: method(API.saveBillingSettings),
	method: 'POST',
	immediate: false,
})

whenTeamReady(() => settings.reload())

const spendAlert = ref(0)

watch(
	() => settings.data,
	(d) => {
		if (d) spendAlert.value = d.spend_alert_threshold ?? 0
	},
	{ immediate: true },
)

// Alert relationship to the current estimate, so the button says something useful
// at a glance and only tints when it's worth noticing.
const crossed = computed(
	() => spendAlert.value > 0 && soFar.value >= spendAlert.value,
)

const near = computed(
	() =>
		spendAlert.value > 0 &&
		!crossed.value &&
		soFar.value >= 0.8 * spendAlert.value,
)

const alertLabel = computed(() => {
	if (spendAlert.value <= 0) return 'Set a budget alert'
	if (crossed.value)
		return `Over your ${money(spendAlert.value, currency.value)} alert`
	if (near.value)
		return `Nearing your ${money(spendAlert.value, currency.value)} alert`

	return `Budget alert at ${money(spendAlert.value, currency.value)}`
})

const alertTint = computed(() =>
	crossed.value ? 'text-ink-red-7' : near.value ? 'text-ink-amber-6' : '',
)

// Dialog: edit against a draft so Cancel leaves the live value untouched.
const dialogOpen = ref(false)
const draft = ref(0)
const formError = ref('')

function openDialog(): void {
	draft.value = spendAlert.value
	formError.value = ''
	dialogOpen.value = true
}

watch(draft, () => (formError.value = ''))

async function submitAlert(): Promise<void> {
	try {
		await saveAlert.submit({
			team: activeTeam.value!,
			spend_alert_threshold: Number(draft.value) || 0,
		})

		if (saveAlert.error) throw saveAlert.error

		spendAlert.value = Number(draft.value) || 0
		successToast('Billing alert saved')
		dialogOpen.value = false
		settings.reload()
	} catch (e) {
		formError.value = getErrorMessage(e, "The budget alert couldn't be saved.")
	}
}
</script>

<template>
	<div
		class="flex flex-col gap-4.5 px-6 pb-6 pt-5.5 transition-colors"
		:class="active ? 'bg-surface-gray-1' : ''"
	>
		<div class="flex items-center justify-between gap-3">
			<h2>
				<component
					:is="hasCycle ? 'button' : 'span'"
					:type="hasCycle ? 'button' : undefined"
					class="flex items-center gap-2 text-sm-medium text-ink-gray-6"
					:class="hasCycle ? 'transition-colors hover:text-ink-gray-8' : ''"
					@click="hasCycle && $emit('open')"
				>
					<span
						class="size-1.5 rounded-9 ring-[3px]"
						:class="
							overLimit
								? 'bg-surface-amber-5 ring-outline-amber-2'
								: 'bg-surface-gray-6 ring-outline-gray-1'
						"
						aria-hidden="true"
					/>
					{{ cycleLabel }}
				</component>
			</h2>
			<Tooltip v-if="canManageBilling" :text="alertLabel">
				<Button label="Budget alerts" @click="openDialog">
					<template #icon>
						<span
							class="lucide-bell size-4"
							:class="alertTint || 'text-ink-gray-6'"
							aria-hidden="true"
						/>
					</template>
				</Button>
			</Tooltip>
		</div>

		<LoadingText v-if="loading" :lines="3" class="w-56" />

		<template v-else>
			<div class="flex flex-wrap items-baseline gap-x-3 gap-y-1">
				<span class="text-8xl-semibold tabular-nums text-ink-gray-9">
					{{ money(soFar, currency) }}
				</span>
				<span class="text-base text-ink-gray-5">
					<template v-if="!hasCycle">nothing billed yet this cycle</template>
					<template v-else>
						so far<template v-if="daysRemaining != null">
							· bills in {{ daysRemaining }} days</template
						>
					</template>
				</span>
			</div>

			<div class="flex flex-col gap-2.5">
				<div class="relative h-2 overflow-hidden rounded-2 bg-surface-gray-2">
					<div
						class="absolute inset-y-0 left-0 bg-surface-gray-10 transition-[width] duration-300"
						:style="{ width: percentOf(withinLimit) }"
					/>
					<div
						v-if="overLimit"
						class="absolute inset-y-0 bg-[repeating-linear-gradient(135deg,var(--surface-amber-5)_0_6px,var(--surface-amber-7)_6px_12px)] transition-[width] duration-300"
						:style="{ left: percentOf(limit), width: percentOf(overLimit) }"
					/>
					<div
						v-if="limit"
						class="absolute inset-y-0 -ml-px w-0.5 bg-surface-base"
						:style="{ left: percentOf(limit) }"
						aria-hidden="true"
					/>
				</div>

				<div
					class="flex flex-wrap justify-between gap-x-3 gap-y-1 text-sm text-ink-gray-6"
				>
					<span v-if="limit">
						Spend limit
						<span class="tabular-nums text-ink-gray-9"
							>{{ money(limit, currency) }}</span
						>
						on
						<button
							type="button"
							class="text-ink-gray-9 underline decoration-dotted underline-offset-[3px] transition-colors hover:decoration-solid"
							@click="$emit('openTiers')"
						>
							{{ tierCurrent?.tier }}
							tier
						</button>
					</span>
					<button
						v-else
						type="button"
						class="text-ink-gray-9 underline underline-offset-[3px]"
						@click="$emit('openTiers')"
					>
						Spend limits
					</button>

					<span v-if="overLimit" class="text-ink-amber-6">
						<span class="tabular-nums">{{ money(overLimit, currency) }}</span>
						over limit
					</span>
				</div>
			</div>
		</template>

		<Dialog v-model:open="dialogOpen" title="Set a budget alert">
			<template #default>
				<Alert v-if="formError" class="mb-4" theme="red" :title="formError" />
				<TextInput
					v-model="draft"
					autofocus
					type="number"
					:label="`Alert me above (${currencySymbol(currency)})`"
					min="0"
					placeholder="20000"
				/>
				<p class="mt-2 text-p-sm text-ink-gray-5">
					We'll notify the team once projected spend crosses this. Set 0 to turn
					it off.
				</p>
			</template>
			<template #actions>
				<div class="flex justify-end gap-2">
					<Button label="Cancel" @click="dialogOpen = false" />
					<Button
						variant="solid"
						label="Set alert"
						:loading="saveAlert.loading"
						@click="submitAlert"
					/>
				</div>
			</template>
		</Dialog>
	</div>
</template>
