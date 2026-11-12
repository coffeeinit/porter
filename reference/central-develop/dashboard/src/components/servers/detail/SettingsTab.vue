<script setup lang="ts">
import { Button } from 'frappe-ui'
import { computed } from 'vue'
import SettingsSection from '@/components/common/SettingsSection.vue'
import type { VirtualMachineRow } from '@/composables/useServers'
import type { ServerActions } from '@/lib/capabilities'
import { isSettingUp } from '@/lib/status'

interface Props {
	server: VirtualMachineRow
	actions: ServerActions
}

const props = defineProps<Props>()

const emit = defineEmits<{
	resize: []
	terminate: []
}>()

const isLocked = computed(
	() => !!props.server.pending_action || isSettingUp(props.server.status),
)
</script>

<template>
	<div class="divide-y divide-outline-gray-1">
		<SettingsSection
			title="Resize server"
			description="Change CPU, memory, or disk size. CPU and memory changes restart the server. Disk size can only increase."
		>
			<template v-if="actions.resize" #actions>
				<Button label="Resize" :disabled="isLocked" @click="emit('resize')" />
			</template>
		</SettingsSection>

		<SettingsSection
			v-if="actions.terminate"
			title="Terminate server"
			description="Permanently deletes the server and its disk. Create a snapshot first to keep a backup."
		>
			<template #actions>
				<Button
					theme="red"
					label="Terminate"
					:disabled="isLocked"
					@click="emit('terminate')"
				/>
			</template>
		</SettingsSection>
	</div>
</template>
