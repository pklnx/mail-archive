---
aside: false
outline: false
---

<script setup>
import OpenAPISpec from "../.vitepress/theme/OpenAPISpec.vue";
import { data as spec } from "./openapi.data.mts";
</script>

# OpenAPI

The OpenAPI 3.1 description of the [JSON API](./api). A running server
serves it as `/api/openapi.json`, without a login; the source is
`internal/web/openapi.yaml` in the repository.

<OpenAPISpec :spec="spec" />
