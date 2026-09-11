# 地层剖面编录与对比服务

`solo-0001-strata-correlation` 是面向地质编录人员的 Go HTTP 后端。它保存岩层深度、岩性和标志层，通过锁定版本获得可追溯的对比输入，再计算共同深度区间中的岩性一致程度。

项目采用 Go 标准库，无第三方 Go 依赖，无外部数据库。运行环境为 Go 1.22+、Linux 或 macOS；工作流冒烟检查额外需要 Python 3.9+。没有前端页面。

## 启动

```bash
go build ./...
go run ./cmd/stratad -addr 127.0.0.1:8093 -data ./data
```

启动成功输出 `STRATA_LISTEN=127.0.0.1:8093`。使用 `-addr 127.0.0.1:0` 可自动选择空闲端口。SIGINT / SIGTERM 会停止接收新请求，等待正在处理的请求结束并释放数据目录锁。

| 参数 | 环境变量 | 默认值 | 用途 |
| --- | --- | --- | --- |
| `-addr` | `STRATA_ADDR` | `127.0.0.1:8093` | HTTP 监听地址 |
| `-data` | `STRATA_DATA` | `./data` | 持久化目录 |
| `-shutdown` | `STRATA_SHUTDOWN` | `10s` | 优雅退出期限，允许 1s–1m |

显式参数优先于环境变量。数据目录只能由一个服务进程打开；系统文件锁在进程退出后自动释放。默认只监听本机，适合受信任的单机部署。当前基线未包含身份验证、TLS 和跨节点复制。

## 快速编录

创建总深度 10 米的剖面。所有深度字段均为**整数毫米**，不接受小数。

```bash
curl -sS http://127.0.0.1:8093/api/v1/profiles \
  -H 'Content-Type: application/json' \
  -d '{"name":"赤石北坡剖面","site":"赤石岭北侧","depth_mm":10000,"note":"砂泥岩层序"}'
```

响应为 HTTP 201，包括 `id`、`version: 1`、`state: "draft"`、`layers: []` 和时间戳。将返回的编号用于下面的 `<profile-id>`。

```bash
curl -sS -X PUT "http://127.0.0.1:8093/api/v1/profiles/<profile-id>/layers" \
  -H 'Content-Type: application/json' \
  -d '{"expected_version":1,"reason":"完成分层编录","layers":[{"top_mm":0,"bottom_mm":4000,"rock":"sandstone","description":"细粒砂岩","marker":""},{"top_mm":4000,"bottom_mm":10000,"rock":"mudstone","description":"灰色泥岩","marker":"凝灰标志"}]}'
```

分层整体替换成功后版本变为 2。输入按顶部深度排序；重叠、非正厚度和越界一律拒绝。空数组可清空草拟剖面，遗漏数组或传入 `null` 会被拒绝。草拟状态允许深度缺口，锁定前必须完整覆盖 `[0, depth_mm)`。

```bash
curl -sS "http://127.0.0.1:8093/api/v1/profiles/<profile-id>/coverage"
curl -sS "http://127.0.0.1:8093/api/v1/profiles/<profile-id>/at?depth_mm=4000"
curl -sS "http://127.0.0.1:8093/api/v1/profiles/<profile-id>/seal" \
  -H 'Content-Type: application/json' \
  -d '{"expected_version":2,"reason":"分层已核对"}'
```

锁定成功返回版本 3。修改必须携带当前 `expected_version`；相同版本的并发请求只有一个成功，其余得到 HTTP 409。`ETag` 仅描述响应版本，写入以 JSON 中的 `expected_version` 为准。已锁定剖面需要通过 `/reopen` 重新打开，新版本不会改变历史记录。

## 对比两个锁定版本

```json
{
  "left": {"id": "<left-profile-id>", "version": 3},
  "right": {"id": "<right-profile-id>", "version": 3},
  "offset_mm": -2000
}
```

将以上对象以 JSON 提交至 `POST /api/v1/comparisons`。右侧深度转换为 `右侧原深度 + offset_mm`，左侧作为共同坐标。算法合并两侧分层边界，并保留每个共同区间的岩性、厚度与 `equal / different / unknown` 关系。

`similarity = equal_mm / known_mm`。未知岩性不计入 `known_mm`；全部共同区间未知时 `similarity` 为 `null`。无共同区间、非锁定输入或同一版本自身对比会被拒绝。两份不同版本可以属于同一剖面。对比结果引用**指定历史版本**，当前剖面重新打开后仍可重用旧结果。

相同输入和算法版本生成同一编号，首次返回 HTTP 201，重复请求返回 HTTP 200 和原结果。交换左右或修改偏移属于不同输入。`GET /api/v1/comparisons/{id}/csv` 导出固定字段的区间 CSV，字段只包含数值、岩性代码和关系代码。

`POST /api/v1/comparison-offsets` 接收 `left`、`right` 引用，根据共同标志层给出偏移建议。标志层按忽略大小写的名称匹配，采用各标志层所需偏移的中位数；偶数项采用中间两项平均并向零取整。响应含证据、残差、是否存在分歧，以及可直接提交的 `comparison` 对象。建议不会自动创建对比结果；这是辅助地层校对的几何计算，不会推断地质年代或自动确定地层对应关系。

## HTTP 接口

接口统一前缀 `/api/v1`，请求体类型 `application/json`，最大 4 MiB；拒绝未知 JSON 字段及多个连续 JSON 对象。应用错误采用 `{"error":{"code":"invalid","field":"depth_mm","detail":"..."}}`。HTTP 422 表示字段错误，409 表示状态或版本冲突，404 表示资源缺失，415 表示请求类型错误，413 表示体积超限，500 表示内部故障。不存在的路由和不支持的方法使用 Go HTTP 的 404/405 响应。

| 方法与路径 | 输入或行为 |
| --- | --- |
| `GET /healthz` | 就绪状态，持久化故障时为 503 |
| `GET /diagnostics` | 剖面、版本、对比用量与快照体积诊断，体积按真实落盘文件口径（含校验和包装） |
| `GET /rocks` | 岩性代码与中文名称 |
| `POST /profiles` | `name, site, depth_mm, note` |
| `GET /profiles` | `q, site, state, rock, offset, limit` 筛选与分页 |
| `GET /profiles/{id}` | 当前完整剖面 |
| `PUT /profiles/{id}` | `expected_version, reason, metadata` 整体替换元数据 |
| `PUT /profiles/{id}/layers` | `expected_version, reason, layers` 整体替换分层 |
| `GET /profiles/{id}/coverage` | 缺口、各岩性厚度和能否锁定 |
| `GET /profiles/{id}/at` | 必填 `depth_mm`，可选 `version`；返回所属层或缺口 |
| `POST /profiles/{id}/seal` | `expected_version, reason`，锁定当前版本 |
| `POST /profiles/{id}/reopen` | `expected_version, reason`，重新打开 |
| `GET /profiles/{id}/history` | 按版本升序列出事件，支持 `offset, limit` |
| `GET /profiles/{id}/revisions/{version}` | 指定历史版本及事件 |
| `GET /profiles/{id}/diff` | 必填 `from, to`，查看同一剖面从旧版本到新版本的差异 |
| `POST /comparison-offsets` | 根据共同标志层建议偏移 |
| `POST /comparisons` | `left, right, offset_mm`，生成或复用对比 |
| `GET /comparisons` | 可选 `profile_id, offset, limit` |
| `GET /comparisons/{id}` | 已保存的完整对比结果 |
| `GET /comparisons/{id}/csv` | 区间 CSV |

上表只有 `/healthz` 位于前缀外。查询字段 `q` 匹配剖面名称，`site` 匹配地点，两者采用不区分大小写的子串匹配。省略 `state` 返回所有状态。列表按更新时间倒序、编号升序稳定排列。分页默认 20、最大 100 条，越过尾端返回空数组；列表接口拒绝未知和重复查询字段。

单个剖面最多 500 层、500 个历史版本，总深度最大 1000000 毫米。最多 2000 个剖面、10000 个对比结果，总快照上限 64 MiB。岩性支持 `sandstone / mudstone / limestone / shale / conglomerate / unknown`。名称最多 120 字、地点 200 字、说明 2000 字，单层描述 1000 字，标志层名称 80 字，修订理由 1–500 字。标志层名称在同一剖面内忽略大小写后必须唯一。

差异接口按完整深度区间匹配分层；边界变化展示为原区间移除和新区间增加，相同区间中的岩性或描述修改展示前后值。深度查询使用左闭右开区间，边界点属于其下方分层，剖面底端不属于任何层。

## 持久化与恢复

数据目录保存 `strata.json` 和 `strata.lock`。每次成功写入将全部状态写到同目录临时文件，执行文件 fsync 后原子替换快照，并对目录执行 fsync。剖面、版本事件和对比结果始终处于同一状态边界；失败的校验不会修改内存或磁盘。

写入在创建任何临时文件之前，先按最终落盘形态序列化整份状态并计算字节数：快照上限按 `strata.json` 的真实文件口径计量，即包含 SHA-256 校验和信封（`{"digest":...,"data":...}`）的完整字节数，而不是其中状态数据本体的字节数。超限（或 2000 剖面、500 版本、10000 对比任一数量上限）在提交前直接拒绝，返回 413 或 409，不创建临时文件、不替换旧快照、不推进内存状态。`GET /api/v1/diagnostics` 的 `snapshot.file_bytes` 与写入闸门使用同一个计量函数：报告值等于磁盘上 `strata.json` 的字节数（首次写入前为按当前状态算出的落盘字节数），另给出数据本体 `data_bytes`、上限与剩余空间，不会出现诊断显示未满而同一写入被体积上限拒绝的矛盾。

快照包含 SHA-256 校验值。启动时检查校验值、版本连续性、状态转换及对比可重复性，遇到损坏拒绝启动。进程在替换前中断保留旧快照，替换后中断使用新快照。同目录遗留的 `.strata-*` 临时文件不会参与恢复，可在服务停止时清理。若替换后同步目录失败，服务保留新内存状态并停止后续写入，健康状态变为 503；检查磁盘并重启后再读取版本确认结果，不要盲目重放修改。

该存储方案针对小规模资料，使用整份快照和内存副本，读写开销随历史体积增长。未实现数据删除、自动清理历史、备份轮替或多进程共享。备份时停止服务后复制数据目录；不要人工修改快照内容。

## 构建与验证

```bash
go build ./...
go vet ./...
python3 scripts/smoke.py all
STRATA_SMOKE_RACE=1 python3 scripts/smoke.py seal
```

**测试模式为 `deferred`**：当前初始化基线有意不生成单元测试、测试数据或专用测试套件；后续“代码测试”任务补充这些内容。`scripts/smoke.py` 是有超时的运行验证入口，它在临时目录编译并启动真实 HTTP 服务、通过本机回环 HTTP 连接完成操作，然后关闭服务并清理临时数据。不会访问外网或修改现有数据目录。也可分别运行 `record / seal / compare / browse` 四个流程。

验证覆盖编录成功与深度失败边界、锁定与重新打开、并发版本冲突、历史不变性、数据目录独占、重启恢复、区间相似度、未知岩性、偏移建议、CSV、列表筛选与分页。

## 目录

```text
cmd/stratad/          启动、信号和 HTTP 服务生命周期
internal/api/        JSON、路由、查询参数和请求记录
internal/catalog/    编录、修订、查阅和对比工作流
internal/geology/    分层规则、覆盖、深度查询和版本差异
internal/correlation/区间对比、标志层偏移与 CSV
internal/persistence/快照校验、原子替换、独占锁
internal/config/     环境变量与启动参数
scripts/smoke.py      临时环境 HTTP 运行验证
```
