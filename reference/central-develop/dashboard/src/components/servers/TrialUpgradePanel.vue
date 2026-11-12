<script setup lang="ts">
import { Alert, call } from 'frappe-ui'
import { computed, ref } from 'vue'
import { API } from '@/api/methods'
import AddMethodDialog from '@/components/AddMethodDialog.vue'
import EditBillingProfileDialog from '@/components/billing/EditBillingProfileDialog.vue'
import { useBillingSetup } from '@/composables/useBillingSetup'
import { useSession } from '@/composables/useSession'
import { getErrorMessage } from '@/lib/feedback'
import { formatDate, money, plural } from '@/lib/format'
import type { ServerTrial } from '@/types/billing'

interface Props {
	trial: ServerTrial
	checking: boolean
	serverId: string
}

const props = defineProps<Props>()
const emit = defineEmits<{ check: [] }>()

const { requireSetup, setupDialogOpen } = useBillingSetup()
const { activeTeam } = useSession()
const showAddMethod = ref(false)
// A new card is confirmed by the gateway first, so the trial can stay open for a moment.
const methodAdded = ref(false)

const title = computed(() =>
	props.trial.state === 'Ended'
		? 'Your free trial has ended'
		: `Your free trial ends in ${plural(props.trial.days_left, 'day')}`,
)

const planName = computed(() =>
	props.trial.plan ? `the ${props.trial.plan.title} plan` : 'its paid plan',
)

const description = computed(() => {
	if (props.trial.state !== 'Ended' || !props.trial.deletes_on)
		return `Add a payment method to keep this site on ${planName.value}. You can choose a bigger plan next.`
	const deletesOn = formatDate(props.trial.deletes_on)
	return props.trial.can_keep
		? `This site is stopped. Keep it before ${deletesOn} to start it again on ${planName.value}.`
		: `This site is stopped. Add a payment method before ${deletesOn} to keep it on ${planName.value}.`
})

const price = computed(() => {
	const plan = props.trial.plan
	if (!plan?.currency || !plan.monthly_rate) return null
	return money(plan.monthly_rate, plan.currency, { trimTrailingZeros: true })
})

const facts = computed(() => [
	{
		icon: 'lucide-credit-card',
		text: price.value
			? `Billing starts today at ${price.value} a month. We charge this card with your monthly invoice.`
			: 'We charge this card for your monthly bill.',
	},
	{
		icon: 'lucide-shield-check',
		text: 'Your site and its data stay as they are.',
	},
])

function addMethod(): void {
	if (requireSetup()) showAddMethod.value = true
}

function onMethodAdded(): void {
	methodAdded.value = true
	emit('check')
}

const keeping = ref(false)
const keepError = ref<string | null>(null)

async function keep(): Promise<void> {
	keeping.value = true
	keepError.value = null
	try {
		await call(API.keepTrialSite, {
			team: activeTeam.value,
			resource_id: props.serverId,
		})
		emit('check')
	} catch (error) {
		keepError.value = getErrorMessage(error)
	} finally {
		keeping.value = false
	}
}

// The dialog's footer holds the button, so it reads these.
defineExpose({ addMethod, methodAdded, keep, keeping })
</script>

<template>
	<div class="space-y-4">
		<Alert
			:theme="trial.state === 'Ended' ? 'red' : 'blue'"
			:title="title"
			:description="description"
		/>
		<Alert
			v-if="trial.held"
			theme="amber"
			title="This site stays on its free trial"
			description="Your payment method is added, but this site's plan is above your spending limit. Contact support to raise the limit."
		/>
		<Alert
			v-if="keepError"
			theme="red"
			title="Couldn't keep this site"
			:description="keepError"
		/>

		<ul class="space-y-3">
			<li
				v-for="fact in facts"
				:key="fact.icon"
				class="flex items-start gap-3 text-p-sm text-ink-gray-7"
			>
				<span
					:class="fact.icon"
					class="mt-0.5 size-4 shrink-0 text-ink-gray-5"
					aria-hidden="true"
				/>
				{{ fact.text }}
			</li>
		</ul>

		<p v-if="methodAdded" class="text-p-sm text-ink-gray-6" aria-live="polite">
			We are confirming your payment method. This can take a minute.
		</p>

		<AddMethodDialog v-model="showAddMethod" @done="onMethodAdded" />
		<EditBillingProfileDialog v-model="setupDialogOpen" />
	</div>
</template>
