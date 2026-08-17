<script setup lang="ts">
import { Connection, Setting } from '@element-plus/icons-vue'
import { ElButton, ElIcon, ElTag } from 'element-plus'
</script>

<template>
  <div class="app-shell">
    <header class="topbar">
      <div class="brand">
        <span class="brand-mark" aria-hidden="true">S</span>
        <span>StackPilot</span>
      </div>
      <ElButton :icon="Setting" text aria-label="设置" disabled />
    </header>

    <main class="content" aria-labelledby="page-title">
      <section class="page-heading">
        <div>
          <p class="eyebrow">本机控制面</p>
          <h1 id="page-title">系统总览</h1>
        </div>
        <ElTag type="info" effect="plain">未连接</ElTag>
      </section>

      <section class="empty-state" aria-live="polite">
        <ElIcon :size="28"><Connection /></ElIcon>
        <h2>控制面尚未连接</h2>
        <p>服务端接入完成后，已注册系统将在这里显示。</p>
      </section>
    </main>
  </div>
</template>
