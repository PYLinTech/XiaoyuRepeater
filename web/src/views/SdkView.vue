<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import CopyButton from '../components/CopyButton.vue'
import Icon from '../components/Icon.vue'
import SegmentedControl from '../components/SegmentedControl.vue'
import { api } from '../api'
import type { SDKInfo } from '../types'

const info = ref<SDKInfo | null>(null)
const infoError = ref(false)

const version = computed(() => info.value?.version || '1')
const protocol = computed(() => info.value?.protocol || 1)
const filename = computed(() => info.value?.filename || `XiaoyuRepeater_SDK_v${version.value}.zip`)
const downloadPath = computed(() => info.value?.download_path || '/api/sdk/download')

const language = ref('go')
const languageTabs = [
  { value: 'go', label: 'Go' },
  { value: 'node', label: 'Node.js' },
  { value: 'python', label: 'Python' },
]

const snippets: Record<string, string> = {
  go: `cd go
go run ./cmd/consumer -name "demo room"`,
  node: `cd node
npm install
node consumer.mjs -name "demo room"`,
  python: `cd python
python3 -m venv .venv
. .venv/bin/activate
pip install -r requirements.txt
python consumer.py --name "demo room"`,
}

const envSnippet = `export XR_API_KEY="你的 API Key"
# 跨环境部署时覆盖当前地址
export XR_SERVER="https://relay.example.com"`

const snippet = computed(() => snippets[language.value])

const packages = [
  {
    name: 'Go',
    runtime: '1.24+',
    path: 'go/',
    icon: 'code',
    desc: '房间管理与不透明 metadata、使用方通道、鉴权回调与轨道订阅',
  },
  {
    name: 'Node.js',
    runtime: '18+',
    path: 'node/',
    icon: 'javascript',
    desc: 'ES Module，随包声明 ws 依赖',
  },
  {
    name: 'Python',
    runtime: '3.10+',
    path: 'python/',
    icon: 'terminal',
    desc: 'asyncio + websockets，支持同步或异步回调',
  },
  {
    name: 'curl',
    runtime: '示例',
    path: 'curl/',
    icon: 'terminal',
    desc: 'HTTP 控制面命令与常见错误码',
  },
]

const steps = [
  {
    title: '设置 API Key',
    desc: '写入使用方程序的服务端环境变量，密钥不会进入 SDK 包。',
  },
  {
    title: '确认中继地址',
    desc: 'config.json 已写入当前地址；跨环境部署时用 XR_SERVER 覆盖。',
  },
  {
    title: '运行示例',
    desc: '示例创建房间并打印用户加入地址，同时保持鉴权通道在线。',
  },
  {
    title: '接入业务',
    desc: '分发房间地址，在自己的 authorize 回调中校验用户令牌。',
  },
]

async function loadInfo() {
  try {
    info.value = await api<SDKInfo>('/api/sdk/version')
    infoError.value = false
  } catch {
    infoError.value = true
  }
}

onMounted(loadInfo)
</script>

<template>
  <div>
    <header class="page-head">
      <div>
        <h1 class="page-title">SDK 下载</h1>
        <p class="page-sub">使用方程序接入中继所需的客户端包，包含 Go、Node.js、Python 与 curl 示例。</p>
      </div>
      <a class="btn btn-primary" :href="downloadPath" :download="filename">
        <Icon name="download" :size="16" />
        下载 SDK 包
      </a>
    </header>

    <div v-if="infoError" class="card sdk-error">
      <span>版本信息暂时读取失败，仍可直接下载当前 SDK 包。</span>
      <button class="btn btn-ghost btn-sm" type="button" @click="loadInfo">立即重试</button>
    </div>

    <section class="card sdk-hero">
      <div class="sdk-hero-main">
        <div class="sdk-mark">
          <Icon name="archive" :size="24" />
        </div>
        <div class="sdk-copy">
          <div class="sdk-title-row">
            <h2 class="sdk-title">XiaoyuRepeater SDK</h2>
            <span class="tag tag-accent">v{{ version }}</span>
          </div>
          <p class="sdk-desc">
            控制面 SDK：创建与查询房间、透传房间 metadata、保持使用方通道、实时鉴权与订阅 WebRTC 轨道。不接触用户数据和媒体内容。
          </p>
          <div class="sdk-meta">
            <span>控制协议 v{{ protocol }}</span>
            <span class="meta-sep">·</span>
            <span>4 种客户端示例</span>
            <span class="meta-sep">·</span>
            <span>解压后按说明运行</span>
          </div>
        </div>
      </div>
      <div class="sdk-file">
        <span class="sdk-file-label">下载文件</span>
        <span class="download-file" :title="filename">{{ filename }}</span>
      </div>
    </section>

    <div class="sdk-grid">
      <section class="card sdk-panel">
        <div class="sdk-panel-head">
          <h2 class="sdk-panel-title">包内容</h2>
          <span class="sdk-panel-sub">按使用方程序的技术栈选择</span>
        </div>
        <ul class="lang-list">
          <li v-for="item in packages" :key="item.name" class="lang-row">
            <span class="lang-icon">
              <Icon :name="item.icon" :size="17" />
            </span>
            <div class="lang-text">
              <div class="lang-name">
                {{ item.name }}
                <span class="lang-runtime">{{ item.runtime }}</span>
              </div>
              <div class="lang-desc" :title="item.desc">{{ item.desc }}</div>
            </div>
            <code class="lang-path">{{ item.path }}</code>
          </li>
        </ul>
      </section>

      <section class="card sdk-panel">
        <div class="sdk-panel-head">
          <h2 class="sdk-panel-title">接入流程</h2>
          <span class="sdk-panel-sub">四步完成</span>
        </div>
        <ol class="steps">
          <li v-for="(step, index) in steps" :key="step.title" class="step">
            <span class="step-num">{{ index + 1 }}</span>
            <div>
              <div class="step-title">{{ step.title }}</div>
              <div class="step-desc">{{ step.desc }}</div>
            </div>
          </li>
        </ol>
      </section>
    </div>

    <section class="card sdk-panel code-panel">
      <div class="sdk-panel-head">
        <div>
          <h2 class="sdk-panel-title">快速接入</h2>
          <p class="sdk-panel-sub">先设置密钥，再选一种语言运行示例。</p>
        </div>
      </div>

      <div class="code-section">
        <div class="code-label">1. 设置环境变量</div>
        <div class="code-block">
          <pre><code>{{ envSnippet }}</code></pre>
          <CopyButton :value="envSnippet" />
        </div>
      </div>

      <div class="code-section">
        <div class="code-label-row">
          <div class="code-label">2. 运行示例</div>
          <SegmentedControl v-model="language" :options="languageTabs" />
        </div>
        <div class="code-block">
          <pre><code>{{ snippet }}</code></pre>
          <CopyButton :value="snippet" />
        </div>
      </div>

      <p class="code-note">
        示例默认读取 SDK 根目录 config.json 和 XR_API_KEY；分发房间地址时可在地址中追加自己的 token、name 等参数。
      </p>
    </section>
  </div>
</template>

<style scoped>
.sdk-error {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 14px;
  margin-bottom: 18px;
  padding: 12px 16px;
  color: var(--warn);
  font-size: 12.5px;
}

.sdk-hero {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 24px;
  padding: 24px;
}

.sdk-hero-main {
  display: flex;
  gap: 16px;
  min-width: 0;
}

.sdk-mark {
  display: grid;
  place-items: center;
  width: 48px;
  height: 48px;
  flex-shrink: 0;
  border: 1px solid var(--accent-line);
  border-radius: 12px;
  background: var(--accent-soft);
  color: var(--accent);
}

.sdk-copy {
  min-width: 0;
}

.sdk-title-row {
  display: flex;
  align-items: center;
  gap: 9px;
  flex-wrap: wrap;
}

.sdk-title {
  font-size: 17px;
  font-weight: 600;
}

.sdk-desc {
  max-width: 680px;
  margin-top: 7px;
  font-size: 13px;
  color: var(--text-2);
}

.sdk-meta {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-top: 9px;
  font-size: 12px;
  color: var(--text-3);
}

.meta-sep {
  color: var(--border-strong);
}

.sdk-file {
  display: flex;
  flex-direction: column;
  align-items: flex-end;
  gap: 4px;
  flex-shrink: 0;
}

.sdk-file-label {
  font-size: 11px;
  color: var(--text-3);
}

.download-file {
  max-width: 230px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-family: var(--mono);
  font-size: 11.5px;
  color: var(--text-3);
}

.sdk-grid {
  display: grid;
  grid-template-columns: minmax(0, 1.1fr) minmax(0, 0.9fr);
  gap: 18px;
  margin-top: 18px;
}

.sdk-panel {
  padding: 20px;
}

.sdk-panel-head {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: 12px;
  margin-bottom: 14px;
}

.sdk-panel-title {
  font-size: 14.5px;
  font-weight: 600;
}

.sdk-panel-sub {
  font-size: 12px;
  color: var(--text-3);
}

.lang-list {
  margin: 0;
  padding: 0;
  list-style: none;
}

.lang-row {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 12px 0;
  border-top: 1px solid var(--border);
}

.lang-row:first-child {
  padding-top: 0;
  border-top: 0;
}

.lang-icon {
  display: grid;
  place-items: center;
  width: 32px;
  height: 32px;
  flex-shrink: 0;
  border-radius: 8px;
  background: var(--surface-2);
  color: var(--text-2);
}

.lang-text {
  min-width: 0;
  flex: 1;
}

.lang-name {
  display: flex;
  align-items: center;
  gap: 7px;
  font-size: 13.5px;
  font-weight: 500;
}

.lang-runtime {
  padding: 0 5px;
  border: 1px solid var(--border);
  border-radius: 4px;
  font-size: 10.5px;
  font-weight: 400;
  line-height: 17px;
  color: var(--text-3);
}

.lang-desc {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  margin-top: 2px;
  font-size: 12px;
  color: var(--text-3);
}

.lang-path {
  flex-shrink: 0;
  padding: 2px 6px;
  border: 1px solid var(--border);
  border-radius: 5px;
  background: var(--bg-deep);
  font-family: var(--mono);
  font-size: 11.5px;
  color: var(--text-2);
}

.steps {
  display: flex;
  flex-direction: column;
  gap: 14px;
  margin: 0;
  padding: 0;
  list-style: none;
}

.step {
  display: flex;
  gap: 11px;
}

.step-num {
  display: grid;
  place-items: center;
  width: 24px;
  height: 24px;
  flex-shrink: 0;
  border: 1px solid var(--accent-line);
  border-radius: 50%;
  background: var(--accent-soft);
  color: var(--accent);
  font-size: 11px;
  font-weight: 600;
}

.step-title {
  font-size: 13px;
  font-weight: 500;
}

.step-desc {
  margin-top: 2px;
  font-size: 12px;
  line-height: 1.55;
  color: var(--text-3);
}

.code-panel {
  margin-top: 18px;
}

.code-section + .code-section {
  margin-top: 16px;
}

.code-label-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 14px;
  margin-bottom: 8px;
}

.code-label {
  margin-bottom: 8px;
  font-size: 12px;
  color: var(--text-2);
}

.code-label-row .code-label {
  margin-bottom: 0;
}

.code-block {
  position: relative;
  overflow: hidden;
  border: 1px solid var(--border);
  border-radius: 9px;
  background: var(--bg-deep);
}

.code-block pre {
  overflow-x: auto;
  margin: 0;
  padding: 15px 86px 15px 16px;
  font-family: var(--mono);
  font-size: 12px;
  line-height: 1.7;
  color: #d4d4d4;
  white-space: pre;
}

.code-block :deep(.copy-btn) {
  position: absolute;
  top: 8px;
  right: 8px;
  color: var(--text-3);
}

.code-block :deep(.copy-btn:hover) {
  color: var(--text);
}

.code-note {
  margin-top: 13px;
  font-size: 12px;
  color: var(--text-3);
}

@media (max-width: 900px) {
  .sdk-hero {
    align-items: flex-start;
    flex-direction: column;
  }

  .sdk-file {
    align-items: flex-start;
  }

  .sdk-grid {
    grid-template-columns: 1fr;
  }

  .code-label-row {
    align-items: flex-start;
    flex-direction: column;
  }
}

@media (max-width: 560px) {
  .sdk-hero {
    padding: 18px;
  }

  .sdk-hero-main {
    flex-direction: column;
  }

  .sdk-panel {
    padding: 17px;
  }

  .download-file {
    max-width: 100%;
  }

  .code-block pre {
    padding-right: 16px;
    padding-top: 44px;
  }
}
</style>
