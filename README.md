<p align="center">
  <a href="https://github.com/ccfos/nightingale">
    <img src="doc/img/Nightingale_L_V.png" alt="Nightingale" width="100" />
  </a>
</p>

<p align="center">
  <b>Nightingale 增强版 —— 夜莺监控二次开发分支</b>
</p>

<p align="center">
  <a href="https://github.com/ccfos/nightingale/releases"><img alt="Upstream release" src="https://img.shields.io/github/v/release/ccfos/nightingale" /></a>
  <a href="LICENSE"><img alt="Apache-2.0 license" src="https://img.shields.io/badge/license-Apache--2.0-blue" /></a>
  <img alt="base" src="https://img.shields.io/badge/%E4%B8%8A%E6%B8%B8%E5%9F%BA%E7%BA%BF-v9.2.0-blue" />
</p>

<p align="center">
  <a href="#-本项目新增能力">新增能力</a> ·
  <a href="#快速开始">快速开始</a> ·
  <a href="#-zabbix-深度集成">Zabbix 集成</a> ·
  <a href="#-openobserve-日志数据源增强">OpenObserve</a> ·
  <a href="#-mcp-服务器管理聊天自动调用">MCP 管理</a> ·
  <a href="#部署">部署</a>
</p>

---

本仓库基于 [ccfos/nightingale](https://github.com/ccfos/nightingale)（夜莺）二次开发，基线同步至上游 **v9.2.0**（含 main 分支后续修复）。上游的统一告警引擎、事件处理、通知分发、内置 AI 助手、MCP Server 等能力全部保留，在此之上扩展了三大方向：

1. **Zabbix 深度集成** —— 把 Zabbix 变成夜莺的一个数据源，并在夜莺 UI 内提供 Zabbix 风格的主机查询/趋势图页面；
2. **OpenObserve 日志数据源增强** —— OpenObserve 风格 SQL 查询、方言兼容改写、日志导出、AI 日志分析；
3. **MCP 服务器管理（作为 MCP 客户端）** —— 在 AI 菜单中接入任意远程 MCP 服务器，AI 助手对话时自动调用其工具。

核心告警能力不依赖 AI 与 Zabbix/OpenObserve/MCP 扩展，可按需启用。

## 快速开始

与上游一致。安装 Git 和 Docker Compose 后，启动自带评估栈：

```bash
git clone https://github.com/ccfos/nightingale.git
cd nightingale/docker/compose-bridge
docker compose up -d
```

打开 [http://localhost:17000](http://localhost:17000)，使用 **`root` / `root.2020`** 登录。

从源码构建本分支（Windows 开发机上已验证的全链流程）：

```bash
# 1. 构建前端（仓库 nightingale-fe，产物 pub/）
cd nightingale-fe && npm ci && npm run build

# 2. 前端资产嵌入 + 后端编译（仓库根目录）
./build-with-fe.sh            # 版本号自动读取前端 package.json
./n9e -version                # 验证版本
```

> ⚠️ `build-with-fe.sh` 会把**当前前端仓库**的 pub/ 嵌入二进制（statik），不会拉取官方前端，OpenObserve/Zabbix 等 UI 改动因此得以保留。`front/statik/statik.go` 是生成文件，改动后需重新生成。

首个告警的三步配置（连接数据源 → 建告警规则 → 配通知渠道）与上游一致，参见上游文档 [n9e.github.io/docs](https://n9e.github.io/docs)。

<a id="-本项目新增能力"></a>
## ✨ 本项目新增能力

| 能力 | 说明 | 关键代码 |
| --- | --- | --- |
| Zabbix 数据源插件 | JSON-RPC 客户端 + 查询层，接入统一 `/ds-query` 通道，窗口 > 3 天自动切换 `trend.get` 小时聚合 | `dskit/zabbix`、`datasource/zabbix` |
| Zabbix 主机查询页 | 「筛选 → 主机列表 → 最新数据抽屉 → 单监控项趋势图」主从交互，Zabbix 风格时间轴 | `fe: src/pages/zabbix/` |
| OpenObserve 数据源增强 | OpenObserve 风格 SQL、Org/Stream 选择器、方言改写、日志导出、AI 日志分析 | `datasource/openobserve`、`fe: src/plugins/openobserve/` |
| MCP 服务器管理 | 接入远程 MCP（streamable-http / SSE），AI 对话自动调用外部工具 | `models/mcp_server.go`、`aiagent/mcpclient/`、`fe: src/pages/aiConfig/mcpServers/` |
| AI 对话增强 | 外部工具目录注入系统提示词、日志分析时间戳预处理、防编造约束 | `aiagent/prompt_builder.go` 等 |

<a id="-zabbix-深度集成"></a>
## 📊 Zabbix 深度集成

无需替换现有 Zabbix，也无需迁移任何数据。夜莺通过 Zabbix JSON-RPC API（`api_jsonrpc.php`）直连，数据留在 Zabbix 侧。

### 功能

- **主机查询页**（菜单「Zabbix 监控 → 监控查询」，权限点 `/zabbix/query`）：
  - 筛选栏：数据源 / 主机名称 / IP / 状态 / 主机群组，展示上游 Zabbix 版本连通性 Tag；
  - 主机列表：接口可用性、主机组、问题数徽标（> 0 红色突出），服务端分页；
  - 最新数据抽屉：关键词高亮、筛选预设（数值型/文本/不支持/禁用）、自动刷新、值人性化（B→GB、bps→Kbps）、变化量 ▲/▼ 着色；
  - 单监控项趋势图：UPlotChart 渲染、Zabbix 风格竖排时间轴刻度、最新/平均/最小/最大统计摘要、tooltip 悬停读数（可单击钉住）。
- **数据源查询**：接入统一 `/ds-query` 通道，可在告警规则等场景复用；时间窗 > 3 天自动切换 `trend.get` 小时聚合。

### 配置方式

1. 「数据源管理」新建 **Zabbix** 类型数据源，填写 Zabbix Web 地址与用户名/密码（用于获取 JSON-RPC auth token）；
2. 分配业务组与权限，即可在「Zabbix 监控」菜单使用查询页。

### 后端 API

```
POST /api/n9e/zabbix-version       # 连通性测试（apiinfo.version）
POST /api/n9e/zabbix-hostgroups    # 主机组列表
POST /api/n9e/zabbix-hosts         # 主机列表
POST /api/n9e/zabbix-items         # 监控项列表
POST /api/n9e/zabbix-host-search   # 主机搜索（分页 + 内存过滤）
POST /api/n9e/zabbix-latest-data   # 最新数据
```

> ⚠️ 兼容性备忘（详见 `nightingale-fe/src/pages/zabbix/README.md`）：Zabbix `problem.get`/`event.get` 不支持 `selectHosts`，时间参数为 `time_from`/`time_till`；`apiinfo.version` 调用不能带 `auth`。已在 Zabbix 7.0 验证。

<a id="-openobserve-日志数据源增强"></a>
## 📜 OpenObserve 日志数据源增强

在日志快捷视图（Logs Explorer）中对 OpenObserve 数据源提供接近原厂的体验：

- **OpenObserve 风格 SQL**：日志查询使用 `SELECT ... FROM stream WHERE ...` SQL，而非 LogQL；
- **Org / Stream 选择器**：组织与数据流级联选择（`ListOrgs` / `ListStreams`）；
- **全文检索方言兼容改写**：OpenObserve 的 `match_all('term')` / `str_match` / `re_match` 等全文函数在部分部署（DataFusion schema 校验严格、空流）下直接报错，后端自动改写为 `ILIKE '%...%'` 谓词组合；空流 / 空时间窗探测后降级为永假谓词，返回 0 行而非 400；
- **日志导出**：按当前查询重拉全量（上限 10000 行并提示截断），导出 CSV（带 BOM，Excel 直开）与 XLSX，自动剥离平台内部字段；
- **AI 日志分析**：日志详情抽屉一键送 AI 分析，送检前自动把 epoch 秒/毫秒/微秒/纳秒时间戳换算为可读时间并注入防编造约束，避免模型误算时间或捏造字段。

即时查询入口 URL 参数示例：

```
/log/explorer?data_source_name=openobserve&data_source_id=${id}&query=${query}
```

<a id="-mcp-服务器管理聊天自动调用"></a>
## 🔌 MCP 服务器管理（聊天自动调用）

上游夜莺内置 **MCP Server**（`/mcp` 端点，供外部 AI 助手调用夜莺能力）。本分支补齐了反向能力：**夜莺 AI 助手作为 MCP 客户端**，调用你自建的任意 MCP 服务器。

### 功能

- **MCP 服务器管理页**（AI 菜单 →「MCP 管理」）：
  - 支持远程 **Streamable HTTP** 与 **SSE** 两种传输；
  - 自定义请求头鉴权（如 `Authorization: Bearer ...`，列表页脱敏、详情明文）；
  - 连接测试（实时列举对端工具数与工具名）、启用/停用、超时设置；
  - 启用状态的服务器不可删除，防止误删在用配置。
- **Agent 绑定**：AI Agent 配置中勾选要接入的 MCP 服务器（`mcp_server_ids`），对话时其工具自动注入。
- **聊天自动调用**：外部 MCP 工具与内置工具同等对待——工具 schema 经原生 function calling 下发，模型按需调用，结果回填对话；系统提示词动态注入外部工具目录与防编造条款（未实际调用过的工具不允许宣称已调用）。
- **健壮性**：单源连接失败自动降级（不拖垮其余工具）；SSRF 防护拒绝链路本地地址（如 169.254.169.254）；每次调用独立会话，无状态依赖。

### 配置方式

1. 「AI 运维 → MCP 管理」新建 MCP 服务器：
   - **传输类型**：Streamable HTTP（推荐）或 SSE；
   - **服务器地址**：MCP 端点，如 `http://192.168.1.10:8123/mcp`；
   - **请求头**：鉴权头（如 OpenObserve MCP 的 `Authorization: Bearer <token>`）；
   - **超时时间**：单次工具调用超时（秒）。
2. 点击「测试连接」验证（显示发现的工具清单）；
3. 「AI 运维 → 智能体」编辑 Agent，勾选该 MCP 服务器，保存；
4. 对话测试：

   > 最近一小时 linux_syslog 有多少条错误日志？

   助手会自动选择并调用 OpenObserve MCP 的 `search_logs` / `get_log_volume` 等工具，基于真实返回回答。

### API 示例

```bash
# 测试连接
curl -X POST http://localhost:17000/api/n9e/mcp-servers/test \
  -H 'Authorization: Bearer <token>' -H 'Content-Type: application/json' \
  -d '{"name":"openobserve-system","transport":"streamable-http",
       "url":"http://192.168.1.10:8123/mcp",
       "headers":{"Authorization":"Bearer <mcp-token>"},"timeout":30}'
```

> ⚠️ 陷阱备忘：
> - MCP 服务器需支持**有状态会话**。FastMCP 部署不要加 `--stateless-http`，否则 go-sdk 握手报 `session not found`；
> - n9e 所在网络需能直连 MCP 端点；若走代理，注意代理对请求目标的改写（回环/内网地址建议加入 `NO_PROXY`）；
> - 模型需支持原生 function calling；小模型建议调大上下文窗口（如 Ollama `num_ctx 32768`），否则工具 schema 会被静默截断；
> - **建表**：程序启动时 GORM AutoMigrate 会自动创建 `mcp_server` 表并为 `ai_agent` 补 `mcp_server_ids` 列，无需手工执行 SQL；如用 SQL 脚本管理库表，`docker/migratesql/migrate.sql` 已含对应 DDL（v9 2026-10-18 段）。

## 核心能力（继承自上游）

| 能力 | 说明 |
| --- | --- |
| 多数据源告警 | 用 PromQL、日志查询或 SQL 定义条件，一条规则可作用于同类型多个数据源实例 |
| 团队所有权 | 业务组隔离规则与看板、团队权限、OIDC / OAuth2 / LDAP / CAS 登录 |
| 事件处理与路由 | 屏蔽、订阅、标签富化/改写、条件流水线分发 |
| 告警执行记录 | 评估查询与结果、活跃/历史事件、通知投递记录全链可查 |
| 可复用集成 | 内置采集配置、告警规则、看板模板 |
| 分布式评估 | 多套告警引擎分担规则；`n9e-edge` 就近评估远程数据源 |
| 通知渠道 | Slack、PagerDuty、Discord、Email、Telegram、Mattermost、Jira 等，HTTP Webhook 与脚本自定义 |
| 内置 AI 助手 | 调查告警、排障告警链路、创建/修改规则与看板、Skills 扩展；支持 OpenAI 兼容 / Claude / Gemini / 自托管模型 |
| MCP Server / A2A | `/mcp` 端点暴露读工具（写工具需显式开启）；A2A 委托内置 Agent 执行任务 |

## 部署

| 方式 | 入口 |
| --- | --- |
| Docker Compose 本地评估 | [docker/compose-bridge](docker/compose-bridge/docker-compose.yaml) |
| Linux 二进制（amd64/arm64） | 上游 [Release](https://github.com/ccfos/nightingale/releases) 与 [配置参考](etc/config.toml) |
| Kubernetes | 上游 [n9e-helm](https://github.com/flashcatcloud/n9e-helm) |
| 本分支源码构建 | `build-with-fe.sh`（见[快速开始](#快速开始)） |

生产建议：锁定版本、配置凭据与 TLS、备份元数据库、监控规则评估与通知失败。多实例共享元数据库与 Redis，时序数据用外部存储；嵌入式 TSDB 仅适合单机小规模部署。

## FAQ

**本分支与上游的关系？如何跟进上游更新？**

本分支是个人增强分支，定期 merge 上游 main/tag（已完成 v9.1.0 → v9.2.0 的同步）。上游新功能随 merge 获得；本分支新增的 Zabbix / OpenObserve / MCP 管理代码集中在 `dskit/zabbix`、`datasource/zabbix`、`datasource/openobserve`、`aiagent/mcpclient`、`center/router/router_zabbix.go`、`models/mcp_server.go` 及前端对应目录，冲突面可控。

**需要替换现有采集器或迁移监控数据吗？**

不需要。夜莺直查现有数据源（Prometheus/VictoriaMetrics/Elasticsearch/Loki/ClickHouse/Zabbix/OpenObserve 等），数据留在原存储。

**AI 是必需的吗？模型请求发到哪里？**

不是。告警核心与 AI 解耦。内置助手将提示词与检索到的数据发送到你配置的模型端点（支持内网自托管）；MCP 工具调用由夜莺直连 MCP 服务器，不经过模型服务商。

**Zabbix 集成需要什么版本？**

JSON-RPC API 兼容 Zabbix 5.x/6.x/7.x（已在 7.0 实测），使用用户名/密码鉴权。

**OpenObserve 的 match_all 报错是什么问题？**

部分 OpenObserve 部署对全文函数做严格 schema 校验，空流/空时间窗下 `match_all` 会 400。本分支已自动改写兼容，无需手动处理。

## 上游与社区

- 上游项目：[ccfos/nightingale](https://github.com/ccfos/nightingale)（文档 [n9e.github.io/docs](https://n9e.github.io/docs)）
- 上游社区：[GitHub Discussions](https://github.com/ccfos/nightingale/discussions) · [Issues](https://github.com/ccfos/nightingale/issues) · [Slack](https://join.slack.com/t/n9e/shared_invite/zt-480klxytf-P9qm3C1M87kDmRPp_H30Kw)
- 本分支问题请先在本仓库提 Issue，并附版本、部署方式与复现步骤

夜莺最初由滴滴开发并开源，2022 年捐赠予中国计算机学会开源发展技术委员会（CCF ODTC）。

## License

[Apache License 2.0](LICENSE)
