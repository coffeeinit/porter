<script setup lang="ts">
import { Badge } from 'frappe-ui'
import SubscriptionRowActions from '@/components/billing/SubscriptionRowActions.vue'
import { money } from '@/lib/format'
import { subscriptionTitle } from '@/lib/subscriptions'
import type {
	PayingForItem,
	ServiceRow,
	SubscriptionRow,
} from '@/types/billing'

// One row of "what you're paying for". Lives in its own component because the
// card shows the top few and the tray shows all of them — rendering the row twice
// is how the two quietly drift apart.
defineProps<{
	row: PayingForItem
	currency: string
	canManage: boolean
	busy: string
}>()
defineEmits<{
	open: [sub: SubscriptionRow]
	pause: [sub: SubscriptionRow]
	resume: [sub: SubscriptionRow]
	assignProject: [sub: SubscriptionRow]
}>()

type BadgeTheme = 'gray' | 'red' | 'blue' | 'green' | 'amber' | 'violet'

function serverSubtitle(sub: SubscriptionRow): string {
	const parts: string[] = []
	if (sub.plan_title && sub.plan_title !== subscriptionTitle(sub))
		parts.push(sub.plan_title)
	if (sub.region) parts.push(sub.region)
	return parts.join(' · ') || sub.billing_cycle || 'Monthly'
}
function statusInfo(
	sub: SubscriptionRow,
): { label: string; theme: BadgeTheme } | null {
	if (sub.status === 'Terminated') return { label: 'Terminated', theme: 'red' }
	if (sub.account_standing === 'Suspended')
		return { label: 'Suspended', theme: 'amber' }
	if (!sub.enabled) return { label: 'Paused', theme: 'gray' }
	if (sub.status === 'Stopped') return { label: 'Stopped', theme: 'gray' }
	return null
}
const isTerminated = (sub: SubscriptionRow): boolean =>
	sub.status === 'Terminated'

function showRate(row: { cost: number | null; sub: SubscriptionRow }): boolean {
	if (row.sub.monthly_rate == null || isTerminated(row.sub)) return false
	if (row.cost == null) return true
	return Math.abs(row.sub.monthly_rate - row.cost) >= 0.005
}

function serviceIcon(s: ServiceRow): string {
	const key = `${s.resource_type || ''} ${s.title || ''}`.toLowerCase()
	if (/token|ai/.test(key)) return 'lucide-sparkles'
	if (/pdf|print/.test(key)) return 'lucide-file-text'
	if (/mail/.test(key)) return 'lucide-mail'
	if (/storage|object/.test(key)) return 'lucide-archive'
	return 'lucide-gauge'
}
function usageLabel(s: ServiceRow): string {
	const unit = s.unit || 'units'
	if (s.settlement_mode === 'Prepaid Pack' && s.allowance) {
		const remaining = Math.max(0, s.allowance - s.period_usage)
		return `${remaining.toLocaleString()} / ${s.allowance.toLocaleString()} ${unit} left`
	}
	const included = s.allowance
		? ` of ${s.allowance.toLocaleString()} incl.`
		: ''
	return `${s.period_usage.toLocaleString()} ${unit}${included}`
}
function exhausted(s: ServiceRow): boolean {
	return (
		s.settlement_mode === 'Prepaid Pack' &&
		s.allowance > 0 &&
		s.period_usage >= s.allowance
	)
}
function usagePct(s: ServiceRow): number {
	if (!s.allowance) return 0
	return Math.min(100, Math.round((s.period_usage / s.allowance) * 100))
}
function overAllowance(s: ServiceRow): boolean {
	return s.allowance > 0 && s.period_usage > s.allowance
}
</script>

<template>
	<tr class="transition-colors hover:bg-surface-gray-1">
		<!-- Server -->
		<template v-if="row.kind === 'server'">
			<td class="py-3 pl-5 pr-3">
				<component
					:is="row.sub.gateway_url ? 'button' : 'div'"
					class="group flex min-w-0 items-center gap-2.5 text-left"
					@click="$emit('open', row.sub)"
				>
					<span
						class="lucide-server size-4 shrink-0 text-ink-gray-5"
						aria-hidden="true"
					/>
					<span
						class="truncate text-base text-ink-gray-9"
						:class="row.sub.gateway_url ? 'transition-colors group-hover:text-ink-gray-7' : ''"
					>
						{{ subscriptionTitle(row.sub) }}
					</span>
					<Badge
						v-if="statusInfo(row.sub)"
						:theme="statusInfo(row.sub)!.theme"
						:label="statusInfo(row.sub)!.label"
					/>
				</component>
			</td>
			<td class="px-3 py-3 text-sm text-ink-gray-5">
				<span class="block truncate">{{ serverSubtitle(row.sub) }}</span>
			</td>
			<td class="px-3 py-3 text-right tabular-nums">
				<span
					class="block text-base"
					:class="isTerminated(row.sub) ? 'text-ink-gray-4' : 'text-ink-gray-9'"
				>
					{{ row.cost != null ? money(row.cost, currency) : '—' }}
				</span>
				<span v-if="showRate(row)" class="block text-sm text-ink-gray-5">
					{{ money(row.sub.monthly_rate, currency, { trimTrailingZeros: true }) }}/mo
				</span>
			</td>
			<td class="w-10 py-3 pr-3">
				<SubscriptionRowActions
					:subscription="row.sub"
					:can-manage="canManage"
					:busy="busy === row.sub.name"
					@open="$emit('open', $event)"
					@pause="$emit('pause', $event)"
					@resume="$emit('resume', $event)"
					@assign-project="$emit('assignProject', $event)"
				/>
			</td>
		</template>

		<!-- Metered service -->
		<template v-else>
			<td class="py-3 pl-5 pr-3">
				<div class="flex min-w-0 items-center gap-2.5">
					<span
						class="size-4 shrink-0 text-ink-gray-5"
						:class="serviceIcon(row.service)"
						aria-hidden="true"
					/>
					<span class="truncate text-base text-ink-gray-9">
						{{ row.service.title || row.service.plan }}
					</span>
					<Badge
						v-if="row.service.settlement_mode !== 'Prepaid Pack'"
						label="Metered"
					/>
					<Badge
						v-if="exhausted(row.service)"
						theme="amber"
						label="Exhausted"
					/>
					<Badge
						v-else-if="overAllowance(row.service)"
						theme="amber"
						label="Over"
					/>
				</div>
			</td>
			<td class="px-3 py-3">
				<div class="flex items-center gap-2.5">
					<div
						v-if="row.service.allowance > 0"
						class="h-1 flex-1 overflow-hidden rounded-1 bg-surface-gray-2"
					>
						<span
							class="block h-full"
							:class="overAllowance(row.service) ? 'bg-surface-amber-5' : 'bg-surface-gray-7'"
							:style="{ width: `${overAllowance(row.service) ? 100 : usagePct(row.service)}%` }"
						/>
					</div>
					<span class="whitespace-nowrap text-sm tabular-nums text-ink-gray-5">
						{{ usageLabel(row.service) }}
					</span>
				</div>
			</td>
			<td class="px-3 py-3 text-right text-base tabular-nums text-ink-gray-9">
				{{ money(row.cost ?? 0, currency) }}
			</td>
			<td class="w-10 py-3 pr-3" />
		</template>
	</tr>
</template>
