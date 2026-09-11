import { readFileSync } from "node:fs";
import { defineConfig } from "tsup";

const packageJson = JSON.parse(readFileSync(new URL("./package.json", import.meta.url), "utf8"));
const buildVersion = process.env.LOGMATE_BUILD_VERSION || packageJson.version;

export default defineConfig({
  entry: ["src/index.ts"],
  format: ["esm", "cjs"],
  target: "es2022",
  dts: true,
  sourcemap: true,
  clean: true,
  define: {
    __LOGMATE_VERSION__: JSON.stringify(buildVersion),
  },
});
