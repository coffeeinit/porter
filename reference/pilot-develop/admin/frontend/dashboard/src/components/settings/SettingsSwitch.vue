<script setup lang="ts">
// Switch's default label (regular ink-gray-5) reads as disabled next to a
// SettingsRow. Styled via data-slot hooks: `labelClasses` was removed in
// frappe-ui 1.0.
import { Switch } from 'frappe-ui'

defineOptions({ inheritAttrs: false })

const LABEL_CLASSES =
  "[&_[data-slot='label']]:font-medium [&_[data-slot='label']]:text-ink-gray-8 " +
  "[&_[data-slot='description']]:text-ink-gray-6"
</script>

<template>
  <Switch v-bind="$attrs" :class="LABEL_CLASSES">
    <template v-for="(_, name) in $slots" #[name]="slotProps">
      <slot :name="name" v-bind="slotProps ?? {}" />
    </template>
  </Switch>
</template>
