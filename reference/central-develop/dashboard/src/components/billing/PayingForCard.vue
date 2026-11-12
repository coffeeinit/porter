<script setup lang="ts">
import { Button, LoadingText } from 'frappe-ui'
import { computed } from 'vue'
import { useRouter } from 'vue-router'
import AssignProjectDialog from '@/components/billing/AssignProjectDialog.vue'
import PayingForRow from '@/components/billing/PayingForRow.vue'
import ProjectsRow from '@/components/billing/ProjectsRow.vue'
import { useCapabilities } from '@/composables/useCapabilities'
import { usePayingFor } from '@/composables/usePayingFor'
import { money } from '@/lib/format'
import { subscriptionTitle } from '@/lib/subscriptions'

const VISIBLE = 5

defineEmits<{ open: []; openProjects: [] }>()

const router = useRouter()
const { canManageBilling } = useCapabilities()

const {
	rows,
	loading,
	currency,
	total,
	busy,
	pendingPause,
	pendingAssignProject,
	openServer,
	askPause,
	confirmPause,
	onResume,
	askAssignProject,
	onAssignedProject,
} = usePayingFor()

const visible = computed(() => rows.value.slice(0, VISIBLE))
const hidden = computed(() => Math.max(0, rows.value.length - VISIBLE))
const goToObjectStorage = () => router.push({ name: 'ObjectStorage' })
</script>

<template>
	<section
		aria-labelledby="subscriptions-heading"
		class="overflow-hidden rounded-6 border border-outline-gray-1 bg-surface-base"
	>
		<header class="flex min-h-14 items-center justify-between gap-3 px-5 py-3">
			<h2 id="subscriptions-heading" class="text-base-semibold text-ink-gray-8">
				Subscriptions
				<span
					v-if="rows.length"
					class="ml-1 text-base tabular-nums text-ink-gray-4"
				>
					{{ rows.length }}
				</span>
			</h2>
			<span class="text-sm tabular-nums text-ink-gray-5">
				{{ money(total, currency) }}
				this cycle
			</span>
		</header>

		<LoadingText v-if="loading" :lines="3" class="px-5 pb-5" />

		<div v-else-if="rows.length" class="overflow-x-auto">
			<table class="w-full min-w-max border-collapse">
				<thead>
					<tr class="text-left text-sm text-ink-gray-5">
						<th class="border-y border-outline-gray-1 py-2 pl-5 pr-3">Name</th>
						<th class="w-2/5 border-y border-outline-gray-1 px-3 py-2">
							Usage
						</th>
						<th class="border-y border-outline-gray-1 px-3 py-2 text-right">
							Cost
						</th>
						<th class="w-10 border-y border-outline-gray-1">
							<span class="sr-only">Actions</span>
						</th>
					</tr>
				</thead>
				<tbody class="divide-y divide-outline-gray-1">
					<PayingForRow
						v-for="row in visible"
						:key="row.id"
						:row="row"
						:currency="currency"
						:can-manage="canManageBilling"
						:busy="busy"
						@open="openServer"
						@pause="askPause"
						@resume="onResume"
						@assign-project="askAssignProject"
					/>
				</tbody>
			</table>
			<button
				v-if="hidden"
				type="button"
				class="flex w-full items-center gap-1 border-t border-outline-gray-1 px-5 py-2.5 text-sm text-ink-gray-5 transition-colors hover:bg-surface-gray-1 hover:text-ink-gray-8"
				@click="$emit('open')"
			>
				View all {{ rows.length }}
				<span class="lucide-arrow-right size-3.5" aria-hidden="true" />
			</button>
		</div>

		<div
			v-else
			class="flex flex-wrap items-center justify-between gap-3 border-t border-outline-gray-1 px-5 py-4"
		>
			<p class="text-p-sm text-ink-gray-5">
				Servers and metered services you subscribe to show here with what they
				cost.
			</p>
			<Button
				v-if="canManageBilling"
				label="Open Object Storage"
				@click="goToObjectStorage"
			/>
		</div>

		<ProjectsRow @open="$emit('openProjects')" />

		<ConfirmDialog
			v-model:target="pendingPause"
			title="Pause billing"
			:message="`Pause billing for ${pendingPause ? subscriptionTitle(pendingPause) : ''}? This stops the server/VM and the site(s)/services running on it, and stops charges until you resume.`"
			confirm-label="Pause billing"
			:loading="busy === pendingPause?.name"
			@confirm="confirmPause"
		/>
		<AssignProjectDialog
			v-model:subscription="pendingAssignProject"
			@assigned="onAssignedProject"
		/>
	</section>
</template>
