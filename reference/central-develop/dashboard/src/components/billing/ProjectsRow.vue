<script setup lang="ts">
import { Button, Tooltip } from 'frappe-ui'
import { computed, ref } from 'vue'
import CreateProjectDialog from '@/components/billing/CreateProjectDialog.vue'
import ProjectMembersDialog from '@/components/billing/ProjectMembersDialog.vue'
import RenameProjectDialog from '@/components/billing/RenameProjectDialog.vue'
import { useCapabilities } from '@/composables/useCapabilities'
import { useProjects } from '@/composables/useProjects'
import { plural } from '@/lib/format'

defineEmits<{ open: [] }>()

const {
	projects,
	pendingRename,
	pendingManageMembers,
	manageMembersOpen,
	reloadProjects,
} = useProjects()

const { canManageBilling } = useCapabilities()

const count = computed(() => projects.data?.length ?? 0)
const showCreate = ref(false)
</script>

<template>
	<div
		class="flex flex-wrap items-center gap-x-3.5 gap-y-2 border-t border-outline-gray-1 px-5 py-3.5"
	>
		<span
			class="lucide-layers size-4 shrink-0 text-ink-gray-5"
			aria-hidden="true"
		/>
		<p class="min-w-0 flex-[1_1_18rem] text-p-sm text-ink-gray-6">
			<span class="text-ink-gray-8">Projects</span>
			<template v-if="count"> · {{ plural(count, 'project') }}.</template>
			Group invoice costs by customer or environment.
			<Tooltip text="Group subscription costs and set project limits.">
				<span
					class="lucide-info ml-0.5 inline-block size-3.5 align-[-2px] text-ink-gray-4"
					aria-label="About projects"
				/>
			</Tooltip>
		</p>
		<div class="flex items-center gap-2">
			<Button
				v-if="count"
				variant="outline"
				label="View all"
				@click="$emit('open')"
			/>
			<Button
				v-if="canManageBilling"
				icon-left="lucide-plus"
				label="New project"
				variant="outline"
				@click="showCreate = true"
			/>
		</div>

		<CreateProjectDialog v-model="showCreate" @created="reloadProjects" />
		<RenameProjectDialog
			v-model:project="pendingRename"
			@saved="reloadProjects"
		/>
		<ProjectMembersDialog
			v-model:open="manageMembersOpen"
			:project="pendingManageMembers"
			@changed="reloadProjects"
		/>
	</div>
</template>
