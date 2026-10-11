import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { parse } from "yaml";

// The spec the server serves as /api/openapi.json; read from the Go package
// so there is one source.
const file = fileURLToPath(new URL("../../internal/web/openapi.yaml", import.meta.url));

export default {
  watch: [file],
  load() {
    return parse(readFileSync(file, "utf8"));
  },
};
