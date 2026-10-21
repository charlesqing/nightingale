# 二开版本部署教程（构建与部署完整指南）

本教程面向需要从源码构建并部署**本二开分支**的用户，覆盖「环境准备 → 获取源码 → 前端构建 → 后端构建 → 部署运行 → 验证」全流程。上游夜莺的通用部署文档参见 [n9e.github.io/docs](https://n9e.github.io/docs)，本教程只补充二开版本特有的部分。

---

## 0. 先理解本仓库的构建模型

本分支由**两个仓库**组成，构建时缺一不可：

| 仓库 | 内容 | 构建产物 |
| --- | --- | --- |
| 后端仓库（本仓库） | Go 后端 + Zabbix/OpenObserve 数据源插件 + MCP 客户端 + `build-with-fe.sh` | `./n9e` 单文件二进制 |
| 前端仓库（`nightingale-fe`） | React/Vite 前端 + Zabbix 查询页 + OpenObserve 日志浏览器 + MCP 管理页 | `pub/` 静态资产目录 |

两个仓库的关系：

- 前端 `npm run build` 产出静态资产到前端仓库的 `pub/`；
- `build-with-fe.sh` 把该 `pub/` 复制到后端仓库的 `pub/`，再用 `statik` 工具生成 `front/statik/statik.go`（把静态资产变成 Go 代码），最后 `go build` 时一并编译进二进制；
- 因此**最终交付物只有一个 `n9e` 二进制文件**，部署目标机不需要 Node/npm。

> 关于 `pub/` 目录的常见疑问：后端仓库里的 `pub/` 就是**已构建好的前端产物副本**（`index.html` + `assets/` + `js/` 等）。它随仓库提交，是一个"快照"——如果前端仓库有新改动但你没有重新执行构建脚本，后端仓库里的 `pub/` 和 `front/statik/statik.go` 就是过期的。**改了前端代码必须重跑 `build-with-fe.sh`（或手动 4 步链）**，否则 UI 变化不会生效。

---

## 1. 环境准备

### 构建机（需要编译）

| 工具 | 版本要求 | 说明 |
| --- | --- | --- |
| Go | **≥ 1.25**（`go.mod` 声明 1.25.0） | [go.dev/dl](https://go.dev/dl/) |
| Node.js | ≥ 18（实测 Node 20/22） | [nodejs.org](https://nodejs.org/) |
| npm | 随 Node 附带 | — |
| Git | 任意近期版本 | — |
| statik | 无需预装 | 脚本检测到缺失时自动 `go install github.com/rakyll/statik@latest` |

> 依赖下载加速（国内网络可选）：
>
> ```bash
> go env -w GOPROXY=https://goproxy.cn,direct
> npm config set registry https://registry.npmmirror.com
> ```

### 部署机（只运行二进制）

- 2C4G 起步（小规模）；二进制无需任何运行时依赖；
- **MySQL ≥ 5.7（建议 8.0）**：存用户、业务组、告警规则、MCP 配置等元数据；
- **Redis ≥ 5.0**：会话、告警引擎心跳、JWT；
- 时序数据不落夜莺——由你已有的 Prometheus / VictoriaMetrics / Elasticsearch / Loki / ClickHouse / Zabbix / OpenObserve 等提供，夜莺只做查询与告警评估。

> 单机快速体验可用嵌入式 TSDB（`etc/config.toml` 保持默认），但仅适合小规模。

---

## 2. 获取源码（两个仓库都要）

```bash
mkdir -p /opt/n9e-build && cd /opt/n9e-build

# 后端（本仓库，分支 feat-mcp / feat-openobserve 均包含全部二开能力）
git clone <本仓库地址> nightingale

# 前端（独立仓库）
git clone <前端仓库地址> nightingale-fe
```

**目录约定**：`build-with-fe.sh` 默认在**后端仓库的上一级目录**下找 `nightingale-fe`（即前端仓库与后端仓库互为同级目录）。例如后端在 `/opt/n9e-build/nightingale`，前端就应放在 `/opt/n9e-build/nightingale-fe`。如果实际布局不同，用环境变量显式指定：

```bash
FE_ROOT=/path/to/nightingale-fe ./build-with-fe.sh
```

---

## 3. 构建前端

```bash
cd nightingale-fe
npm ci          # 按 package-lock.json 精确安装依赖
npm run build   # 产物输出到本仓库的 pub/ 目录
```

验证：`ls pub/index.html` 存在、`ls pub/assets/` 有 `index-*.js` 即成功。

---

## 4. 构建后端（一键脚本）

```bash
cd ../nightingale
./build-with-fe.sh            # 版本号自动读取前端 package.json
# 或显式指定：
./build-with-fe.sh v9.2.0
```

脚本内部执行 4 步（也可手动逐步执行以便排查）：

```bash
# 1) 前端构建（若上一步已完成可跳过）
( cd ../nightingale-fe && npm run build )

# 2) 同步前端产物到后端 pub/（先清空再复制，避免旧资产残留）
rm -rf pub && mkdir pub
cp -r ../nightingale-fe/pub/. pub/

# 3) statik 嵌入（必须在后端仓库根目录、用相对路径调用）
go install github.com/rakyll/statik@latest
"$(go env GOPATH)/bin/statik" -src=pub -dest=front -f

# 4) 编译二进制并注入版本号
FE_VERSION="$(node -e "console.log(require('../nightingale-fe/package.json').version)")"
go build -ldflags "-X github.com/ccfos/nightingale/v6/pkg/version.Version=$FE_VERSION" -o n9e ./cmd/center/main.go
```

验证：

```bash
./n9e -version    # 输出 v9.2.0（或你指定的版本）
```

产物：`./n9e`（Linux 下无 `.exe` 后缀；Windows 下为 `n9e.exe`）。

> ⚠️ **不要用上游的 `fe.sh`**：它会重新 clone 官方 n9e/fe 并覆盖 `pub/`，把本分支的 OpenObserve / Zabbix / MCP 前端改动全部冲掉。本分支统一走 `build-with-fe.sh`。

---

## 5. Windows / Linux 平台兼容性

`build-with-fe.sh` 是 POSIX bash 脚本，**Linux 与 macOS 原生可跑**；Windows 下**必须用 Git Bash（MSYS2）执行**（安装 Git for Windows 自带），不要用 cmd/PowerShell 直接跑。

已处理的跨平台细节：

| 细节 | 说明 |
| --- | --- |
| `cygpath` 转换 | 脚本用 `cygpath -m` 把 MSYS 风格路径（`/d/ai/...`）转成 Windows 程序能识别的形式（`D:/ai/...`）再传给 node；Linux 上没有 cygpath，脚本已做回退（原样传递 unix 路径），两端通用 |
| `statik` 路径 | statik 对绝对 unix 路径在 Windows 下会误解成 `C:\d\...`，脚本固定 `cd` 到后端根目录后用**相对路径** `pub`/`front` 调用，规避该坑 |
| 产物命名 | Windows 下 `go build -o n9e` 实际产出 `n9e.exe`，`./n9e -version` 会自动解析 |

Linux 原生环境（推荐的生产构建方式）无需任何改动，直接 `./build-with-fe.sh`。

> 如需交叉编译（如在 Windows 构建机产出 Linux 二进制）：`GOOS=linux GOARCH=amd64 go build ...`，静态资产嵌入不受影响。

---

## 6. 部署运行

### 6.1 准备目录与配置

```bash
# 部署机
mkdir -p /opt/n9e/{etc,logs}
cp n9e /opt/n9e/

# 从源码仓库拷贝运行所需的 etc 内容：
# config.toml 主配置；metrics.yaml 数据源类型定义（启动时 cconf 读取）；
# script 告警执行脚本目录
cp -r etc/config.toml etc/metrics.yaml etc/script /opt/n9e/etc/
cd /opt/n9e
```

编辑 `/opt/n9e/etc/config.toml` 关键段（注意：本版本 `[DB]` 用 **DSN** 而非分离的 Host/User 键；默认 `DBType = "sqlite"`，生产改 MySQL）：

```toml
[HTTP]
Host = "0.0.0.0"
Port = 17000

[DB]
DBType = "mysql"
DSN = "n9e:你的密码@tcp(127.0.0.1:3306)/n9e_v6?charset=utf8mb4&collation=utf8mb4_general_ci&parseTime=True&loc=Local"

[Redis]
Address = "127.0.0.1:6379"
RedisType = "standalone"   # 默认 "miniredis"（进程内内存 Redis，仅单进程）；连接外部 Redis 必须显式改 standalone
```

初始化数据库（先在源码仓库把 SQL 拷到部署机；二开会新增的 `mcp_server` 表等**无需手工建**，见 6.4 说明）：

```bash
# 在源码 checkout 目录执行拷贝：
scp docker/initsql/a-n9e.sql <部署机>:/tmp/
# 在部署机执行初始化（脚本自带 create database n9e_v6）：
mysql -h 127.0.0.1 -u root -p < /tmp/a-n9e.sql
```

> - 只需导入 `a-n9e.sql` 一个脚本即可（`c-init.sql` 是 Docker MySQL 镜像初始化 root 密码用的，二进制部署不需要）；
> - 脚本不包含上游 AI 模块的其余表（`ai_llm_config` / `ai_skill` / `assistant_*` 等，上游 initsql 固有缺口），首次启动 n9e 时 GORM AutoMigrate 会自动补齐——**导入后必须启动一次 n9e 再开始使用**。

### 6.2 systemd 托管（生产推荐）

`/etc/systemd/system/n9e.service`：

```ini
[Unit]
Description=Nightingale Monitoring (fork)
After=network-online.target mysql.service redis.service
Wants=network-online.target

[Service]
Type=simple
WorkingDirectory=/opt/n9e
ExecStart=/opt/n9e/n9e
Restart=always
RestartSec=5
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
```

```bash
systemctl daemon-reload
systemctl enable --now n9e
systemctl status n9e
```

> 若部署机上存在 HTTP 代理且你使用 MCP 管理 / 内网自托管模型，务必在 unit 的 `[Service]` 段加上：
>
> ```ini
> Environment=NO_PROXY=127.0.0.1,localhost,<你的内网MCP地址>
> Environment=no_proxy=127.0.0.1,localhost,<你的内网MCP地址>
> ```
>
> 代理会改写 n9e 发出的 MCP 请求目标（绝对形式 URI），导致远端 404。

### 6.3 Docker Compose（快速体验）

在**源码 checkout 目录**执行（compose-bridge 会拉起 MySQL + Redis + VictoriaMetrics + Categraf + n9e 全套）：

```bash
cd docker/compose-bridge
docker compose up -d
```

> compose-bridge 里的 n9e 镜像是上游官方镜像 `flashcatcloud/nightingale:latest`，**不含二开能力**。要体验二开功能，把其中 nightingale 服务的 `image:` 换成基于本教程产出的二进制自建镜像，或直接改用二进制 + systemd 部署。自建镜像 Dockerfile 思路：`FROM alpine` → 拷贝 `n9e` 与 `etc/` → `ENTRYPOINT ["/n9e"]`。

### 6.4 数据库初始化流程与二开表结构同步说明

**二进制部署的完整初始化流程**（MySQL 为例）：

```bash
# 1) 准备库与账号（可跳过，a-n9e.sql 自带 create database n9e_v6）
mysql -u root -p -e "CREATE DATABASE IF NOT EXISTS n9e_v6 DEFAULT CHARSET utf8mb4;
                     CREATE USER IF NOT EXISTS 'n9e'@'%' IDENTIFIED BY '你的密码';
                     GRANT ALL ON n9e_v6.* TO 'n9e'@'%';"

# 2) 导入基础表（含全部上游表 + 二开新增表）
mysql -u root -p n9e_v6 < a-n9e.sql

# 3) 启动 n9e —— AutoMigrate 自动补齐 initsql 未覆盖的表（上游 AI 模块其余表）
#    并校准已有表结构；首次启动后即可正常使用
```

**SQLite**（`etc/config.toml` 保持 `DBType = "sqlite"`）：数据库文件 `DSN` 指定的 `n9e.db` 不存在时由程序自动创建并 AutoMigrate 全量建表，也可预先执行 `docker/sqlite.sql`。**PostgreSQL**：导入 `docker/compose-postgres/initsql_for_postgres/a-n9e-for-Postgres.sql` 后启动。**存量库升级**（上游 v9.x → 本分支）：`docker/migratesql/migrate.sql` 已含二开全部 DDL（`v9 2026-10-18` 段），可选执行或直接启动让 AutoMigrate 自动补齐。

**二开新增模块的表结构同步情况**（已核对模型定义 `models/mcp_server.go`、`models/ai_agent.go` 与全部 SQL 脚本）：

| 脚本 | `mcp_server` 表 | `ai_agent` 表（含二开新增 `mcp_server_ids` 列） | 说明 |
| --- | --- | --- | --- |
| `docker/initsql/a-n9e.sql`（MySQL 全新部署） | ✅ 本次补充 | ✅ 本次补充 | 上游 initsql 原本连 `ai_agent` 都没有；本次追加了 `ai_agent`（带 `mcp_server_ids` text 列，`idx_name`/`idx_use_case` 索引）与 `mcp_server`（`idx_name` 索引）两个 CREATE TABLE |
| `docker/sqlite.sql`（SQLite 全新部署） | ✅ 本次补充 | ✅ 本次补充 | 按 SQLite 方言（`integer primary key autoincrement` + 独立 `CREATE INDEX`）追加同两张表 |
| `docker/compose-postgres/initsql_for_postgres/a-n9e-for-Postgres.sql` | ✅ 本次补充 | ✅ 本次补充 | 按 PostgreSQL 方言（`bigserial PRIMARY KEY` + 独立 `CREATE INDEX`）追加同两张表 |
| `docker/migratesql/migrate.sql`（存量库升级） | ✅ 二开时已含 | ✅ 二开时已含 | `v9 2026-10-18` 段的 MySQL DDL，与模型定义逐列一致 |

DDL 与 GORM 模型的对应关系已逐列核对：`mcp_server` 的 12 个持久化列（id/name/description/transport/url/headers/timeout/enabled/created_at/created_by/updated_at/updated_by）及默认值（`transport` 默认 `streamable-http`、`timeout` 默认 30、`enabled` 默认 0）与 `models/mcp_server.go` 完全一致；`ai_agent.mcp_server_ids` 为 `text` 类型存 JSON 数组（`serializer:json`），与 `models/ai_agent.go:22` 一致。**冗余兜底**：即便使用不含这些 DDL 的旧脚本初始化，AutoMigrate 也会在启动时自动建表/补列，因此该同步是"脚本与模型对齐"而非功能必需。

---

## 7. 部署后验证清单

| 步骤 | 命令/操作 | 预期 |
| --- | --- | --- |
| 版本 | `./n9e -version` | v9.2.0（或你注入的版本号） |
| 进程 | `systemctl status n9e` / `GET /` | active (running) / HTTP 200 |
| 前端资产 | 浏览器打开 `http://<ip>:17000` | 登录页正常，样式完整（F12 无 404） |
| 默认账号 | `root` / `root.2020` 登录 | 进入首页 |
| 二开菜单 | 左侧菜单 | 存在「Zabbix 监控」「AI 运维 → MCP 管理」 |
| Zabbix | 数据源管理 → 新建 Zabbix 数据源 → 连通性 Tag | 显示上游 Zabbix 版本号 |
| MCP | MCP 管理 → 测试连接 | 弹窗显示「发现 N 个工具」 |
| AI 助手 | 对话提问触发 MCP 工具 | 消息记录 `executed_tools: true` |

## 8. 常见问题（构建/部署阶段）

**Q：页面能打开但是白屏 / 样式错乱？**
二进制里嵌的还是旧 `pub/`。重跑 `build-with-fe.sh` 全链（前端有改动时四步都要走），重启进程后浏览器 Ctrl+F5 清缓存。

**Q：`go build` 报 `no required module provides package ... aws-sdk-go/aws`？**
GOMODCACHE 中该依赖损坏或版本漂移。删除 `$(go env GOMODCACHE)/github.com/aws/aws-sdk-go@<ver>` 后 `go mod download`；若报版本不一致，`go list -m <mod>` 查看实际选中版本后 `go get <mod>@<ver>`。

**Q：`npm ci` 失败？**
优先确认 Node ≥ 18、npm 缓存完整（`npm cache clean --force` 后重试）；国内网络配置 npmmirror 镜像。

**Q：Windows 下脚本跑到 `rm -rf pub` 或 statik 一步失败？**
确认使用的是 Git Bash 而非 cmd/PowerShell；杀毒软件/安全策略可能拦截批量删除，重试或手动清理 `pub/` 后再跑脚本。

**Q：构建机是 Windows，目标机是 Linux？**
推荐直接在 Linux 构建机（或容器内）执行 `./build-with-fe.sh`，避免交叉编译与路径问题；确实需要交叉编译见第 5 节说明。
