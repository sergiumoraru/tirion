<template>
  <div role="status" class="flex items-center justify-center gap-2 py-6 font-mono text-accent">
    <span aria-hidden="true" class="loading-ring" />
    <span class="text-sm">Loading...</span>
  </div>
</template>

<style scoped>
.loading-ring {
  width: 1rem;
  height: 1rem;
  flex: 0 0 1rem;
  border: 2px solid currentColor;
  border-right-color: transparent;
  border-radius: 50%;
  animation: loading-spin 800ms linear infinite;
}

@keyframes loading-spin {
  to { transform: rotate(360deg); }
}

@media (prefers-reduced-motion: reduce) {
  .loading-ring { animation: none; }
}
</style>
