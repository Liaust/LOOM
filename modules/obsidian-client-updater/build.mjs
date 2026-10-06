import { build } from "esbuild";
await build({ entryPoints: ["src/main.ts"], outfile: "main.js", bundle: true,
    format: "cjs", platform: "browser", target: "es2020", external: ["obsidian"], minify: true });
