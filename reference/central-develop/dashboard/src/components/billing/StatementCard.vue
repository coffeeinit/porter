<script setup lang="ts">
import { Badge, Button, LoadingText } from 'frappe-ui'
import { computed } from 'vue'
import { billingPeriod } from '@/lib/date'
import { money } from '@/lib/format'
import { invoiceTheme } from '@/lib/status'
import type { Statement } from '@/types/billing'

const VISIBLE = 5
const props = defineProps<{
	statement: Statement | null
	loading: boolean
	exportUrl: string
}>()
defineEmits<{ open: [] }>()

const currency = computed(() => props.statement?.currency ?? 'INR')
const rows = computed(() =>
	(props.statement?.rows ?? []).slice(-VISIBLE).reverse(),
)
const hidden = computed(() =>
	Math.max(0, (props.statement?.rows?.length ?? 0) - VISIBLE),
)
</script>

<template>
	<section
		aria-labelledby="statement-heading"
		class="overflow-hidden rounded-6 border border-outline-gray-1 bg-surface-base"
	>
		<header class="flex min-h-14 items-center justify-between gap-3 px-5 py-3">
			<h2
				id="statement-heading"
				class="flex min-w-0 items-baseline gap-2 text-base-semibold text-ink-gray-8"
			>
				Statement
				<span v-if="statement" class="truncate text-sm text-ink-gray-5">
					{{ billingPeriod(statement.from_date, statement.to_date) }}
				</span>
			</h2>
			<div class="flex shrink-0 items-center gap-2">
				<Button
					v-if="statement && hidden"
					variant="ghost"
					:label="`View all ${statement.rows.length}`"
					@click="$emit('open')"
				/>
				<Button
					v-if="statement?.rows?.length"
					icon="lucide-download"
					label="Export CSV"
					:href="exportUrl"
				/>
			</div>
		</header>

		<LoadingText v-if="loading" :lines="3" class="px-5 pb-5" />

		<p
			v-else-if="!statement"
			class="border-t border-outline-gray-1 px-5 py-4 text-p-sm text-ink-gray-5"
		>
			Couldn't load the statement.
		</p>

		<p
			v-else-if="!rows.length"
			class="border-t border-outline-gray-1 px-5 py-4 text-p-sm text-ink-gray-5"
		>
			No invoices in this period.
		</p>

		<table v-else class="w-full border-collapse">
			<thead>
				<tr class="text-left text-sm text-ink-gray-5">
					<th class="border-y border-outline-gray-1 py-2 pl-5 pr-3">Period</th>
					<th class="border-y border-outline-gray-1 px-3 py-2">Paid</th>
					<th class="border-y border-outline-gray-1 px-3 py-2">Status</th>
					<th class="border-y border-outline-gray-1 py-2 pl-3 pr-5 text-right">
						Amount
					</th>
				</tr>
			</thead>
			<tbody class="divide-y divide-outline-gray-1">
				<tr
					v-for="row in rows"
					:key="row.invoice"
					class="transition-colors hover:bg-surface-gray-1"
				>
					<td class="py-3 pl-5 pr-3 text-base text-ink-gray-9">
						{{ billingPeriod(row.period_start, row.period_end) }}
					</td>
					<td class="px-3 py-3 text-sm tabular-nums text-ink-gray-5">
						{{ money(row.amount_paid + row.credit_applied, currency) }}
					</td>
					<td class="px-3 py-3">
						<Badge :theme="invoiceTheme(row.status)" :label="row.status" />
					</td>
					<td
						class="py-3 pl-3 pr-5 text-right text-base tabular-nums text-ink-gray-9"
					>
						{{ money(row.total, currency) }}
					</td>
				</tr>
			</tbody>
		</table>

		<p
			v-if="!loading && statement"
			class="border-t border-outline-gray-1 px-5 py-3 text-p-sm text-ink-gray-5"
		>
			For reconciling against your own records. Tax invoices are issued
			separately.
		</p>
	</section>
</template>
