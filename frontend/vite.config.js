import { copyFileSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { resolve } from "node:path";
import { defineConfig } from "vite";

const source = resolve("src/node-mode");

export default defineConfig({
  build: {
    target: "chrome94",
  },
  plugins: [{
    name: "node-mode-assets",
    configureServer(server) {
      const routes = {
        "/node-mode/index.html": [resolve(source, "index.html"), "text/html; charset=utf-8"],
        "/node-mode/main.js": [resolve(source, "main.js"), "text/javascript; charset=utf-8"],
        "/node-mode/style.css": [resolve(source, "style.css"), "text/css; charset=utf-8"],
        "/node-mode/embed.css": [resolve("src/node-mode-embed.css"), "text/css; charset=utf-8"],
      };
      server.middlewares.use((request, response, next) => {
        const route = routes[String(request.url || "").split("?")[0]];
        if (!route) return next();
        let content = readFileSync(route[0], "utf8");
        if (String(request.url || "").startsWith("/node-mode/index.html")) {
          content = content.replace("</head>", '<link rel="stylesheet" href="./embed.css" /></head>');
        }
        response.statusCode = 200;
        response.setHeader("Content-Type", route[1]);
        response.setHeader("Cache-Control", "no-store");
        response.end(content);
      });
    },
    closeBundle() {
      const target = resolve("dist/node-mode");
      const revision = Date.now().toString(36);
      mkdirSync(target, { recursive: true });
      const html = readFileSync(resolve(source, "index.html"), "utf8")
        .replace('href="./style.css"', `href="./style.css?v=${revision}"`)
        .replace('src="./main.js"', `src="./main.js?v=${revision}"`)
        .replace("</head>", `<link rel="stylesheet" href="./embed.css?v=${revision}" /></head>`);
      writeFileSync(resolve(target, "index.html"), html, "utf8");
      copyFileSync(resolve(source, "main.js"), resolve(target, "main.js"));
      copyFileSync(resolve(source, "style.css"), resolve(target, "style.css"));
      copyFileSync(resolve("src/node-mode-embed.css"), resolve(target, "embed.css"));
    },
  }],
});
