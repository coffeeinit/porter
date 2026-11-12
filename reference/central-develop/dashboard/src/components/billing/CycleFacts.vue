<script setup lang="ts">
import { LoadingText, Tooltip } from 'frappe-ui'
import { computed, ref } from 'vue'
import AddMethodDialog from '@/components/AddMethodDialog.vue'
import { useBillingOverview } from '@/composables/useBillingOverview'
import { useBillingSetup } from '@/composables/useBillingSetup'
import { useCapabilities } from '@/composables/useCapabilities'
import { shortDate } from '@/lib/date'
import { formatDate, money } from '@/lib/format'

interface Props {
	outstanding: number
	scheduleActive: boolean
	walletActive: boolean
}

const props = defineProps<Props>()

defineEmits<{ openSchedule: []; openWallet: []; addCredit: [] }>()

const { nextPayment, credit, forecast, methods, currency, reloadMethods } =
	useBillingOverview()

const { canManageBilling } = useCapabilities()
const { requireSetup } = useBillingSetup()

const loading = computed(
	() =>
		(nextPayment.loading && !nextPayment.data) ||
		(credit.loading && !credit.data),
)

const hasOutstanding = computed(() => props.outstanding > 0)
const nextCharge = computed(() => Number(nextPayment.data?.amount ?? 0))

const amountDue = computed(() =>
	hasOutstanding.value ? props.outstanding : nextCharge.value,
)

const blocker = computed(() =>
	nextCharge.value > 0 ? nextPayment.data?.blockers?.[0] : undefined,
)

const dueHint = computed(() => {
	if (blocker.value) return blocker.value.title
	if (hasOutstanding.value || !nextCharge.value) return ''

	const chargedOn = `Charged ${shortDate(nextPayment.data?.charge_on)}`
	const method = nextPayment.data?.method?.label

	return method ? `${chargedOn} from ${method}` : chargedOn
})

const balance = computed(() => Number(credit.data?.balance ?? 0))
const projected = computed(() => Number(forecast.data?.projected_total ?? 0))

const workingMethod = computed(() =>
	(methods.data ?? []).find((method) => method.status === 'Active'),
)

const short = computed(
	() => projected.value > 0 && balance.value < projected.value,
)

const atRisk = computed(() => short.value && !workingMethod.value)

const coverage = computed(() => {
	if (!projected.value) return ''
	if (!short.value) return 'Covers this cycle in full'

	const percent = Math.floor((balance.value / projected.value) * 100)
	const covers = `Covers ${percent}% of this cycle`

	return atRisk.value ? covers : `${covers}, card pays the rest`
})

const expiry = computed(() => {
	const grant = credit.data?.expiring?.[0]

	if (!grant) return ''

	const amount = money(grant.amount, currency.value)

	return `${amount} expires ${formatDate(grant.expires_on)}`
})

const walletHint = computed(() =>
	[coverage.value, expiry.value].filter(Boolean).join('. '),
)

const primaryMethod = computed(() => workingMethod.value ?? methods.data?.[0])
const nextInvoiceOn = computed(() => shortDate(forecast.data?.period_end))

const showAddMethod = ref(false)

const onAddMethod = () => {
	if (requireSetup()) showAddMethod.value = true
}
</script>

<template>
	<dl class="flex flex-col justify-between gap-4 px-5 pb-6 pt-7 text-base">
		<LoadingText v-if="loading" :lines="4" class="py-2" />
		<template v-else>
			<div class="flex items-center justify-between gap-3">
				<dt>
					<button
						v-if="nextCharge > 0"
						type="button"
						class="transition-colors hover:text-ink-gray-8"
						:class="scheduleActive ? 'text-ink-gray-9' : 'text-ink-gray-5'"
						@click="$emit('openSchedule')"
					>
						{{ hasOutstanding ? 'Due now' : 'Next payment' }}
					</button>
					<span v-else class="text-ink-gray-5">
						{{ hasOutstanding ? 'Due now' : 'Next payment' }}
					</span>
				</dt>
				<dd class="flex items-center gap-2">
					<Tooltip :text="dueHint" :disabled="!dueHint">
						<span class="text-base-medium tabular-nums text-ink-gray-9">
							{{ money(amountDue, currency) }}
						</span>
					</Tooltip>
				</dd>
			</div>

			<div class="flex items-center justify-between gap-3">
				<dt>
					<button
						type="button"
						class="transition-colors hover:text-ink-gray-8"
						:class="walletActive ? 'text-ink-gray-9' : 'text-ink-gray-5'"
						@click="$emit('openWallet')"
					>
						Wallet
					</button>
				</dt>
				<dd class="flex items-center gap-2">
					<!-- Prepaid credit is the second way to pay; the card stays the main one. -->
					<button
						v-if="canManageBilling"
						type="button"
						class="text-p-sm text-ink-blue-link transition-opacity hover:opacity-80"
						@click="$emit('addCredit')"
					>
						Add credit
					</button>
					<Tooltip :text="walletHint" :disabled="!walletHint">
						<span class="text-base-medium tabular-nums text-ink-gray-9">
							{{ money(balance, currency) }}
						</span>
					</Tooltip>
				</dd>
			</div>

			<div class="flex items-center justify-between gap-3">
				<dt class="text-ink-gray-5">Payment method</dt>
				<dd class="min-w-0 truncate">
					<span v-if="primaryMethod" class="text-ink-gray-8">
						{{ primaryMethod.display_label || primaryMethod.method_type }}
					</span>
					<button
						v-else-if="canManageBilling"
						type="button"
						class="text-ink-blue-link transition-opacity hover:opacity-80"
						@click="onAddMethod"
					>
						Add card
					</button>
					<span v-else class="text-ink-gray-5">None</span>
				</dd>
			</div>

			<div class="flex items-center justify-between gap-3">
				<dt class="text-ink-gray-5">Next invoice</dt>
				<dd class="tabular-nums text-ink-gray-8">{{ nextInvoiceOn || '—' }}</dd>
			</div>
		</template>
		<AddMethodDialog v-model="showAddMethod" @done="reloadMethods" />
	</dl>
</template>
