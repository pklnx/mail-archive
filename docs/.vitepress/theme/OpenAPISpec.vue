<script lang="ts">
let installed = false;
</script>

<script setup lang="ts">
import { type Component, getCurrentInstance, onMounted, shallowRef } from "vue";

// vitepress-openapi is about 2 MB of JavaScript. It is loaded here, in the
// browser and only on the page that shows the spec, instead of in the theme,
// which every page loads.
const props = defineProps<{ spec: object }>();
const app = getCurrentInstance()!.appContext.app;
const OASpec = shallowRef<Component | null>(null);

onMounted(async () => {
  const plugin = await import("vitepress-openapi/client");
  await import("vitepress-openapi/dist/style.css");
  if (!installed) {
    // Registers its components and its i18n plugin.
    await plugin.theme.enhanceApp({ app } as Parameters<typeof plugin.theme.enhanceApp>[0]);
    installed = true;
  }
  OASpec.value = plugin.OASpec;
});
</script>

<template>
  <component :is="OASpec" v-if="OASpec" :spec="props.spec" />
  <p v-else>Loading the API description…</p>
</template>
