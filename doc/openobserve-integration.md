# OpenObserve 数据源接入指南（Nightingale v9）

> 适用范围：在 Nightingale v9（`github.com/ccfos/nightingale` 本仓库）新增 `openobserve` 日志数据源后端插件，并在前端仓库 `n9e/fe` 完成类型注册与日志检索 UI 接入。
>
> 背景：OpenObserve 原生只兼容 Elasticsearch / Loki 的**摄取侧**，查询侧是它自己的 SQL（DataFusion）。因此**不能**直接复用现有 `elasticsearch` / `loki` 数据源类型，必须新增独立插件，走 OpenObserve 原生 SQL 查询 API（`POST /api/{org}/_search?type=logs`）。后端输出已对齐 Loki 的归一化日志结构，便于前端复用 Loki 日志浏览器。

---

## 一、后端（本仓库，已完成）

| 文件 | 改动 |
|---|---|
| `datasource/openobserve/openobserve.go` | 新增插件，实现 `datasource.Datasource` 接口；调 OpenObserve SQL `_search` API，HTTP Basic 认证；日志输出复用 `dskit/loki` 的 `QueryResponse` / `NormalizeLogs` 保证前端兼容 |
| `center/router/router_openobserve.go` | 4 个路由处理器 + `getOpenObserve` 辅助 |
| `center/cconf/plugin.go` | `Plugins` 追加 id=12：`Category:"logging"`, `Type:"openobserve"`, `TypeName:"OpenObserve"` |
| `center/router/router.go` | 注册 4 条路由：`/openobserve-label-names`、`/openobserve-label-values`、`/openobserve-parsed-fields`、`/openobserve-histogram` |

> 时间窗处理：前端传毫秒，插件内 ×1000 转 OpenObserve 所需的**微秒**。
> `query` 约定：当作 SQL 的 WHERE 布尔片段（如 `level = 'ERROR' AND service = 'auth'`）；若以 `select` 开头则视为完整 SQL（自含时间过滤）。

**日志告警如何工作（关键）：** 告警引擎对 OpenObserve 走 `eval.go` 的 `default` 分支 → `GetAnomalyPoint → plug.QueryData`（**不是** `QueryLog`，`QueryLog` 仅用于日志浏览器/预览）。`QueryData` 因此必须产出数值序列：
- 告警引擎调用 `QueryData` 时**不传 `start/end`**（由评估时刻决定），插件已补默认窗口（最近 60s），否则 `BETWEEN 0 AND 0` 查不到、告警永不触发。
- 若 `query` 是 WHERE 片段（日志告警最常见）：包成 `SELECT count(*) AS count FROM "<stream>" WHERE _timestamp BETWEEN ... AND ... AND (<片段>)`，返回**单点 count 序列**，触发器按 `count > N` 判断。
- 若 `query` 是完整 `SELECT`（用户自写聚合/时间序列）：直接透传，`rowsToDataResp` 对无时间列的聚合结果用评估时刻兜底成点。
- 前端告警规则预览（`GraphPreview`）走的是 `logQuery`→`QueryLog`，展示匹配的原始日志表，用于校验查询；阈值在 Triggers 里设。这与 victorialogs 行为一致。

> ⚠️ **Review 修复（2026-07-28）：`QueryData` 时间单位 1000 倍错误。** 原默认窗口把"现在"算成 `nowSec*1000`（毫秒）但应为**微秒**（`nowSec*1_000_000`），窗口减数用了 `defaultAlertWindow/time.Millisecond`（=60000 微秒=60ms）应为 `defaultAlertWindow/time.Microsecond`（=60_000_000 微秒=60s）；且两个时间戳回算 `endMicros/1000`（毫秒当秒）应为 `endMicros/1_000_000`。否则日志告警查询窗口仅 ~60ms 且锚点错误 → 几乎永远查不到 → **告警永不触发**。已修正并 `go build` 通过、后端已重启生效。

**后端编译验证：**
```bash
cd /d/ai/nightingale
go build ./datasource/openobserve/... ./center/router/...   # 定向编译 OK
go vet  ./datasource/openobserve/...
go build -o n9e ./cmd/center/main.go                         # 完整构建 OK（2026-07-29，二进制 ~176MB）
```

---

## 二、前端（仓库 `n9e/fe`，已落地 ✅）

> **实现状态**：已克隆 `n9e/fe`（工作区 `D:\ai\nightingale-fe`）并落地下列全部改动。前端类型注册**不是**由后端 `Plugins` 列表驱动，而是由前端硬编码的 `baseCates` 数组决定 —— 因此必须在此注册，下拉框才会出现该项并被视为日志源。

⚠️ **关键纠正（与早期方案差异）**：早期方案曾建议「复制 `Loki` 目录做 OpenObserve 浏览器」。**实际落地改用 `victorialogs` 插件作为模板**，原因：
- VictoriaLogs 走的是**通用** `/api/n9e/logs-query`（与我们的 OpenObserve 后端一样，不是 Loki 专属的 `/proxy/{ds}/api/v1/query_range`）；
- 后端 `dskit/loki.NormalizeLogs` 输出的归一化结构与 VictoriaLogs 浏览器消费的结构**完全一致**，因此复制 VictoriaLogs 插件即可正确渲染 OpenObserve 日志，无需改渲染层。

### 2.1 新建插件目录：`src/plugins/openobserve/`

- 由 `src/plugins/victorialogs/` **整体复制**而来（59 个文件），随后用脚本批量重命名：
  - `victorialogs-field-names/-values` → `openobserve-label-names/-values`，`victorialogs-histogram` → `openobserve-histogram`（即服务层接口前缀对齐后端新增路由）；
  - 全文 `victorialogs` → `openobserve`，`VictoriaLogs` → `OpenObserve`（共 22 个文件）。
- `NAME_SPACE = DatasourceCateEnum.openobserve`（**计算键**），i18n 命名空间自动解析，locale 目录（`zh_CN/zh_HK/en_US/ja_JP/ru_RU`）无需单独补语言文件即可工作。
- `src/plugins/openobserve/constants.ts`：`DEFAULT_QUERY` 改为 `''`（OpenObserve 用 SQL WHERE 片段，不是 VLogs 的 `_time: 1m | ` 语法）。
- `src/plugins/openobserve/Datasource/Form.tsx`：在地址（`http://localhost:5080/`）基础上新增 `Organization` 与 `Stream / Dataset` 两个表单字段（initialValue 均为 `default`）。
- i18n 命名空间键已由 `victorialogs: 'VictoriaLogs'` 类改为计算键，README 标题已改为「OpenObserve 数据源」。

### 2.2 必改：数据源类型注册表（下拉框 + 能力 + 是否日志源）

文件：`src/components/AdvancedWrap/utils.ts` —— 在 `baseCates` 追加（已落地）：

```ts
{
  value: 'openobserve',
  label: 'OpenObserve',
  label_en: 'OpenObserve',
  type: ['logging'],
  alertRule: true,
  dashboard: false,
  dashboardVariable: false,
  graphPro: false,
  alertPro: false,
  logo: '/image/logos/openobserve.png',
},
```

> `value` 必须与后端 `plugin.go` 的 `Type:"openobserve"` 完全一致。

文件：`src/utils/constant.ts` —— `BaseDatasourceCateEnum` 已追加 `openobserve = 'openobserve'`。

### 2.3 Logo 图标

已在 `public/image/logos/openobserve.png` 放置自定义绘制的 **青绿渐变「O」图标**（128×128 透明 PNG，7587 B，由 PIL 基于 OpenObserve 品牌色 `#00B9C1→#018088` 绘制）。**非官方 logo**（网络受限时未能抓取真图），生产环境如需可替换为官方 SVG/PNG。已嵌入正式二进制（见第三节）。

### 2.4 必改：各页面类型分支（全部已落地）

| 页面 / 文件 | 改动 |
|---|---|
| `src/pages/explorer/Explorer.tsx`（旧版浏览器） | 新增 `else if (datasourceCate === openobserve)` → 渲染 `OpenObserve` 插件 Explorer |
| `src/pages/logExplorer/ExplorerContent.tsx`（新版日志浏览器） | 新增 `openobserve` 分支 → 渲染 `OpenObserve/ExplorerNG` |
| `src/pages/logExplorer/constants.ts` | `ENABLED_VIEW_CATES` 追加 `openobserve`（使其在开源版日志浏览器可见） |
| `src/pages/logExplorer/utils/getFormValuesBySearchParams.ts` | `victorialogs` 特殊分支（解析 URL → 表单值，以及表单值 → URL 串）均追加 `\|\| openobserve` |
| `src/pages/logExplorer/components/RenderValue/useFieldConfig.tsx` | Plus 下钻可用判断追加 `isOpenObserveAvailable = cate==='openobserve' && query` |
| `src/pages/datasource/Datasources/Form.tsx` | `params.type === openobserve` → 渲染 `OpenObserve/Datasource/Form` |
| `src/pages/datasource/Datasources/Detail.tsx` | `plugin_type === openobserve` → 渲染 `OpenObserve/Datasource/Detail` |
| `src/pages/event/DetailNG/eventDetailByCate.tsx` | `cate === openobserve` → 渲染 `OpenObserve/Event` |
| `src/pages/alertRules/Form/Rule/Rule/index.tsx`（旧版告警） | `cate === openobserve` → 渲染 `OpenObserve/AlertRule` |
| `src/pages/alertRules/FormNG/Rule/index.tsx`（新版告警） | 同上 |
| `src/pages/alertRules/FormNG/index.tsx` | 排序优先级数组追加 `'openobserve'`（与 victorialogs 并列） |
| `src/pages/alertRules/FormNG/components/Sidebar/RuleSummary.tsx` | 显示名映射追加 `openobserve: 'OpenObserve'` |
| `src/pages/alertRules/Form/utils.ts` | 默认规则值分支追加 `openobserve`（用 `OPENOBSERVE_DEFAULT_QUERY = ''`） |
| `src/pages/landing/landing.data.ts` | 数据源 chips 追加 OpenObserve 卡片 |

> 其余测试夹具（`*.test.ts`）仅含 `victorialogs` 示例数据且通过 mock `allCates` / `DatasourceCateEnum`，不受本次改动影响，未强制同步。

### 2.5 后端⇄前端字段约定（务必一致）

前端共享表单组件（`BasicAuth` / `Headers` / `Cluster`）把配置写成 `settings.<cate>.*`。因此后端结构体 JSON / mapstructure 键**必须**为 `openobserve.*`（而非早期草稿的 `oo.*`）：

```go
Addr        string `json:"openobserve.addr"         mapstructure:"openobserve.addr"`
Org         string `json:"openobserve.org"          mapstructure:"openobserve.org"`
Basic       OpenObserveBasic  `json:"openobserve.basic" mapstructure:"openobserve.basic"`
TLS         OpenObserveTLS    `json:"openobserve.tls"   mapstructure:"openobserve.tls"`
Headers     map[string]string `json:"openobserve.headers" mapstructure:"openobserve.headers"`
Stream      string `json:"openobserve.stream"       mapstructure:"openobserve.stream"`
ClusterName string `json:"openobserve.cluster_name" mapstructure:"openobserve.cluster_name"`
MaxQueryRows int   `json:"openobserve.max_query_rows" mapstructure:"openobserve.max_query_rows"`
```

---

## 三、构建与嵌入

> ⚠️ **不要跑 `fe.sh`**：该脚本会重新 git clone 官方 `n9e/fe` 并覆盖 `pub/`，把我们的 OpenObserve 改动冲掉。必须用下面的**手动嵌入流程**。

在 `n9e/fe` 仓库（工作区 `D:\ai\nightingale-fe`）：
```bash
cd /d/ai/nightingale-fe
npm install                 # node_modules 已就绪可跳过
npm run build               # tsc + vite build，产出 pub/
```

在本仓库（Nightingale 后端）：
```bash
cd /d/ai/nightingale
mkdir -p pub
cp -r /d/ai/nightingale-fe/pub/. ./pub/          # 复制自建前端到后端 pub/
go install github.com/rakyll/statik@latest       # GOPROXY=goproxy.cn 可装
"$(go env GOPATH)/bin/statik" -src=./pub -dest=./front -f   # 重新生成 front/statik/statik.go（覆盖官方前端）
# ⚠️ 必须注入版本号，否则 /api/n9e/version 与 /api/n9e/versions 都返回 "unknown"（前端"关于产品"页、顶部版本角标显示 unknown）
go build -ldflags "-X github.com/ccfos/nightingale/v6/pkg/version.Version=v9.0.0" -o n9e ./cmd/center/main.go
./n9e -version                                    # 验证：应输出 v9.0.0
```
> 重新生成的 `statik.go` 约 87MB（base64 嵌入 60MB 的 `pub/`：32MB 字体 + 24MB JS 资源 + 4.2MB 图片），对应二进制 `n9e` 约 176MB。启动后 17000 端口即 Serve 自建前端，OpenObserve 出现在 UI。

**一键打包脚本：`build-with-fe.sh`**（仓库根目录，2026-07-29 新增并验证通过）。一行命令完成「前端构建 → 复制 pub → statik 嵌入 → go build（含版本 ldflags）」四步，输出带版本的正式二进制。用法：`./build-with-fe.sh`（版本自动读 `../nightingale-fe/package.json`），或 `./build-with-fe.sh v9.0.0` 显式指定。
> ⚠️ 脚本内两处 Windows 路径坑已规避：① statik 是 Windows 原生二进制，必须用**相对路径** `pub`/`front`（绝对 unix 路径 `/d/...` 会被当成 `C:\d\...`）；② 读 fe `package.json` 版本用 `cygpath -m` 转成 `D:/...` 形式，否则 node 解析失败回退 `unknown`。

**嵌入状态（2026-07-29）：✅ 已完成。** `front/statik/statik.go` 已用含 OpenObserve 的前端重新生成，二进制 `n9e`（176MB）已构建并启动于 17000，验证：
- `GET /` → 200
- `GET /image/logos/openobserve.png` → 200（确认是自建资源而非官方前端）
- 主 JS bundle 含 `openobserve` 字符串（确认前端代码已编入）

---

## 四、联调验证清单

- [x] 后端插件骨架（`datasource/openobserve/openobserve.go` + 4 条路由）已落地（`gofmt` 通过，完整 `go build -o n9e` 已通过）
- [x] 前端 `n9e/fe` 已克隆并落地全部类型注册 / 分支 / 插件（见第二节）
- [x] **已验证**：`n9e/fe` 仓库内 `npm run build`（`tsc` 零类型错误 + `vite build` 成功产出 `pub/`），构建通过
- [x] **已验证（2026-07-29）**：前端已通过 statik 嵌入正式二进制，二进制 `n9e` 已启动于 17000 端口，Serve 含 OpenObserve 的前端（`/`→200、`/image/logos/openobserve.png`→200、JS bundle 含 `openobserve`，见第三节）
- [ ] 前端下拉框出现 `OpenObserve`（来自 `baseCates`）
- [ ] 新建 OpenObserve 数据源，填写 URL（如 `http://ip:5080`）与 HTTP Basic 账号密码，「保存并测试」成功
- [ ] 日志浏览器能选到该数据源，能拉到 label / 字段列表（`/openobserve-label-names`、`/openobserve-parsed-fields`）
- [ ] 输入查询（SQL WHERE 片段）能返回日志，直方图正常（`/openobserve-histogram`、日志查询接口）
- [ ] 能基于该数据源创建一条日志告警规则（SQL 周期查询）
- [ ] 确认 OpenObserve `_search` 响应字段名（`hits` / `records` 已做双字段兜底）、`_timestamp` 返回形态（RFC3339 或微秒数值，`ParseTimestampNanos` 已兼容）

---

## 五、已知待真机确认项

1. OpenObserve `_search` 实际响应字段名（本项目用 `hits`/`records` 双字段兜底）。
2. `_timestamp` 字段返回形态（字符串 / 数值），已用 `dskit/loki` 的 `ParseTimestampNanos` 兼容。
3. 直方图 SQL：`date_trunc('minute', _timestamp)` 在目标 OpenObserve 版本是否支持（失败已做「单点总数」兜底）。
4. 前端 `OpenObserve` 目录复制后，需确认 Loki 组件里是否有硬编码的「Loki 专属」逻辑（如 LogQL 语法提示）需要弱化为通用 SQL 提示。
