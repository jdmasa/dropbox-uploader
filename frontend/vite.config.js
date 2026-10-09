import { defineConfig } from 'vite';

export default defineConfig({
  build: {
    // Windows 7/8.1 can only run WebView2 109, so target that Chromium version.
    target: 'chrome109',
    cssTarget: 'chrome109',
  },
});
