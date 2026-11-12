<script setup lang="ts">
import { Button, LoadingText, TabButtons } from 'frappe-ui'
import { BarChart, useChartTokens } from 'frappe-ui/charts'
import { computed, ref } from 'vue'
import BillingCard from '@/components/billing/BillingCard.vue'
import { useBillingOverview } from '@/composables/useBillingOverview'
import {
	BILLING_REPORT_MONTHS,
	type useBillingReports,
} from '@/composables/useBillingReports'
import { useSpendingLimits } from '@/composables/useSpendingLimits'
import { currencySymbol, money } from '@/lib/format'

interface Props {
	reports: ReturnType<typeof useBillingReports>
}

const props = defineProps<Props>()
const { forecast } = useBillingOverview()
const { current: tierCurrent } = useSpendingLimits()

const MONTH_OPTIONS = BILLING_REPORT_MONTHS.map((option) => ({
	...option,
	label: `${option.value}M`,
}))

const SLICE_CLASSES = [
	'bg-surface-gray-10',
	'bg-surface-gray-7',
	'bg-surface-gray-5',
	'bg-surface-gray-4',
	'bg-surface-gray-3',
]

const DISPLAY_LABEL: Record<string, string> = { 'VM Plans': 'Servers' }

const months = computed({
	get: () => props.reports.months.value,
	set: (value: number) => {
		props.reports.months.value = value
	},
})

const history = computed(() => props.reports.history.data)
const currency = computed(() => props.reports.currency.value)

const compactFormat = new Intl.NumberFormat('en', {
	notation: 'compact',
	maximumFractionDigits: 1,
})

const compact = (value: number): string =>
	`${currencySymbol(currency.value)}${compactFormat.format(value).toLowerCase()}`

const stats = computed(() => [
	{ label: 'Invoiced', value: history.value?.total ?? 0 },
	{ label: 'Monthly average', value: props.reports.average.value },
	{
		label: 'Paid by credits',
		value: props.reports.statement.data?.settled_by_credits ?? 0,
	},
	{ label: 'Tax', value: props.reports.tax.data?.total_tax ?? 0 },
])

const limit = computed(() => Number(tierCurrent.value?.max_spend ?? 0))

const chartRows = computed(() => {
	const rows = history.value?.months ?? []

	const accrued = Number(
		forecast.data?.measured ?? forecast.data?.projected_total ?? 0,
	)

	return rows.map((row, index) => {
		const isCurrent = index === rows.length - 1
		const current = isCurrent ? Math.max(row.total, accrued) : 0
		const within = limit.value ? Math.min(current, limit.value) : current

		return {
			month: row.month.endsWith('-01') ? row.month.slice(0, 4) : row.label,
			invoiced: isCurrent ? 0 : row.total,
			current: within,
			over: current - within,
			limit: limit.value,
		}
	})
})

const showLimit = computed(
	() =>
		limit.value > 0 &&
		limit.value <=
			Math.max(
				...chartRows.value.map((row) => row.invoiced + row.current + row.over),
			),
)

const hasOverage = computed(() => chartRows.value.some((row) => row.over > 0))

const seriesKeys = computed(() => [
	'invoiced',
	'current',
	...(hasOverage.value ? ['over'] : []),
	...(showLimit.value ? ['limit'] : []),
])

const { tokens } = useChartTokens(ref())

const themeColor = (name: string): string => {
	void tokens.value

	return getComputedStyle(document.documentElement)
		.getPropertyValue(`--${name}`)
		.trim()
}

const seriesConfig = computed(() => ({
	invoiced: { label: 'Invoiced', color: themeColor('surface-gray-7') },
	current: { label: 'This cycle', color: themeColor('surface-gray-4') },
	over: { label: 'Over limit', color: themeColor('surface-amber-6') },
	limit: {
		label: `Spend limit ${money(limit.value, currency.value, { trimTrailingZeros: true })}`,
		type: 'line' as const,
		dashed: true,
		echartOptions: { lineStyle: { width: 1 } },
		color: themeColor('ink-gray-5'),
	},
}))

const products = computed(() => {
	const rows = history.value?.by_product ?? []
	const total = rows.reduce((sum, row) => sum + row.amount, 0)

	if (total <= 0) return []

	return rows.map((row, index) => ({
		label: DISPLAY_LABEL[row.label] ?? row.label,
		amount: row.amount,
		percent: (row.amount / total) * 100,
		colorClass: SLICE_CLASSES[index % SLICE_CLASSES.length],
	}))
})
</script>

<template>
	<BillingCard title="Spend">
		<template #action>
			<div class="flex items-center gap-2">
				<TabButtons v-model="months" :options="MONTH_OPTIONS" />
				<!-- A period with no invoices exports an empty file. -->
				<Button
					v-if="(history?.invoice_count ?? 0) > 0"
					icon="lucide-download"
					label="Export CSV"
					:href="reports.exportUrl('spend')"
				/>
			</div>
		</template>

		<LoadingText v-if="reports.loading.value" :lines="6" />

		<div
			v-else-if="!history"
			class="flex items-center justify-between gap-3 rounded-5 px-4 py-3"
		>
			<p class="text-p-sm text-ink-gray-6">Couldn't load your spend.</p>
			<Button label="Retry" @click="reports.reload()" />
		</div>

		<p
			v-else-if="reports.neverBilled.value"
			class="rounded-5 px-4 py-3 text-p-sm text-ink-gray-6"
		>
			Your spend, payments and tax show up here after your first invoice.
		</p>

		<template v-else>
			<dl class="grid grid-cols-2 gap-3 sm:grid-cols-4">
				<div
					v-for="stat in stats"
					:key="stat.label"
					class="rounded-5 px-4 py-3.5"
				>
					<dt class="text-sm text-ink-gray-5">{{ stat.label }}</dt>
					<dd
						class="mt-2 text-lg-medium tabular-nums"
						:class="stat.value ? 'text-ink-gray-9' : 'text-ink-gray-5'"
					>
						{{ money(stat.value, currency) }}
					</dd>
				</div>
			</dl>

			<div class="mt-5 flex flex-wrap gap-7">
				<div class="h-64 min-w-0 flex-[999_1_24rem]">
					<BarChart
						:data="chartRows"
						x="month"
						:y="seriesKeys"
						stacked
						:series-config="seriesConfig"
						:y-axis="{ format: compact }"
					/>
				</div>

				<section class="flex min-w-0 flex-[1_1_15rem] flex-col gap-3">
					<div class="flex items-center justify-between">
						<h3 class="text-sm-medium text-ink-gray-6">By product</h3>
						<span class="text-sm tabular-nums text-ink-gray-5">
							{{ money(history.total, currency) }}
						</span>
					</div>
					<template v-if="products.length">
						<div class="flex h-1.5 gap-0.5 overflow-hidden rounded-1">
							<span
								v-for="product in products"
								:key="product.label"
								:class="product.colorClass"
								:style="{ width: `${product.percent}%` }"
							/>
						</div>
						<ul class="flex flex-col">
							<li
								v-for="product in products"
								:key="product.label"
								class="flex items-center gap-2.5 border-b border-outline-gray-1 py-2.5 text-base text-ink-gray-8"
							>
								<span
									class="size-2 shrink-0 rounded-1"
									:class="product.colorClass"
									aria-hidden="true"
								/>
								<span class="min-w-0 flex-1 truncate">{{ product.label }}</span>
								<span class="text-sm tabular-nums text-ink-gray-5">
									{{ Math.round(product.percent) }}%
								</span>
								<span class="w-20 text-right tabular-nums">
									{{ money(product.amount, currency) }}
								</span>
							</li>
						</ul>
					</template>
					<p v-else class="text-p-sm text-ink-gray-5">
						No itemised charges in this period.
					</p>
					<RouterLink
						:to="{ name: 'BillingInvoices' }"
						class="inline-flex items-center gap-1 self-start text-sm text-ink-gray-5 transition-colors hover:text-ink-gray-8"
					>
						View invoices
						<span class="lucide-arrow-right size-3.5" aria-hidden="true" />
					</RouterLink>
				</section>
			</div>
		</template>
	</BillingCard>
</template>
