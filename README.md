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

## 导出锁定资料闭包

对外提交时，只选择锁定剖面版本或已保存对比结果：

```json
{
  "selections": [
    {"type": "profile_revision", "id": "prf_...", "version": 3}
  ]
}
```

`POST /api/v1/exchange-packages` 会规范化并去重选择项，然后按引用关系展开闭包，直到没有新的引用：

- 锁定剖面版本属于一份剖面历史，因此包含该剖面的**全部历史版本和事件**；
- 剖面历史会带出所有引用其中任意版本的对比结果；
- 对比结果会带出其左、右两份剖面历史；
- 成员清单按类型和编号排序，重复请求同一选择集合会复用同一个不可变包。

交换包格式为 `exchange-package/v1`。下载内容包含 `payload`、`manifest`、成员内容摘要和顶层 SHA-256 摘要。若展开时发现引用已不存在，服务仍会生成 `status: "incomplete"`、`complete: false` 的记录，并在 `missing_references` 中逐项给出缺失类型、编号/版本、引用方和引用关系；不会把缺失内容静默省略。`GET .../inspect` 以包内清单为准：`package_closure_complete` 表示包内是否原本有缺失，`service_closure_complete` 表示这些缺失当前是否仍在服务中不存在；成员级状态还会区分仍存在、已变化和当前缺失。

上传检查只读取包，不导入或保存。当前读取器明确支持 `exchange-package/v1`；未来格式升级后，v1 仍会迁移读取。当前版本收到无法理解的 v2+ 或未知格式时，返回 HTTP 422、`valid: false` 和具体原因，不会把摘要不匹配或未知结构当作校验通过。

## HTTP 接口

接口统一前缀 `/api/v1`，请求体类型 `application/json`，常规写入最大 4 MiB；外部包上传检查最大 64 MiB；拒绝未知 JSON 字段及多个连续 JSON 对象。应用错误采用 `{"error":{"code":"invalid","field":"depth_mm","detail":"..."}}`。HTTP 422 表示字段错误，409 表示状态或版本冲突，404 表示资源缺失，415 表示请求类型错误，413 表示体积超限，500 表示内部故障。不存在的路由和不支持的方法使用 Go HTTP 的 404/405 响应。

| 方法与路径 | 输入或行为 |
| --- | --- |
| `GET /healthz` | 就绪状态，持久化故障时为 503 |
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
| `POST /exchange-packages` | `selections`，展开并保存不可变资料交换包 |
| `GET /exchange-packages/{id}` | 包元数据、清单与内容摘要（不含载荷重述） |
| `GET /exchange-packages/{id}/download` | 下载完整交换包 JSON |
| `GET /exchange-packages/{id}/inspect` | 以包内清单为准，只读检查当前服务状态 |
| `POST /exchange-packages/inspect` | 上传旧格式或外部交换包，只读校验并对照当前服务 |

上表只有 `/healthz` 位于前缀外。查询字段 `q` 匹配剖面名称，`site` 匹配地点，两者采用不区分大小写的子串匹配。省略 `state` 返回所有状态。列表按更新时间倒序、编号升序稳定排列。分页默认 20、最大 100 条，越过尾端返回空数组；列表接口拒绝未知和重复查询字段。

单个剖面最多 500 层、500 个历史版本，总深度最大 1000000 毫米。最多 2000 个剖面、10000 个对比结果、1000 个资料交换包，总快照上限 64 MiB。岩性支持 `sandstone / mudstone / limestone / shale / conglomerate / unknown`。名称最多 120 字、地点 200 字、说明 2000 字，单层描述 1000 字，标志层名称 80 字，修订理由 1–500 字。标志层名称在同一剖面内忽略大小写后必须唯一。

差异接口按完整深度区间匹配分层；边界变化展示为原区间移除和新区间增加，相同区间中的岩性或描述修改展示前后值。深度查询使用左闭右开区间，边界点属于其下方分层，剖面底端不属于任何层。

## 持久化与恢复

数据目录保存 `strata.json` 和 `strata.lock`。资料交换包作为不可变成员随同一快照持久化；后续新增剖面、修订版本或对比结果只影响新的包，不会改变既有包的载荷、成员清单或摘要。每次成功写入将全部状态写到同目录临时文件，执行文件 fsync 后原子替换快照，并对目录执行 fsync。剖面、版本事件、对比结果和交换包始终处于同一状态边界；失败的校验不会修改内存或磁盘。

快照包含 SHA-256 校验值。启动时检查校验值、版本连续性、状态转换及对比可重复性，遇到损坏拒绝启动。进程在替换前中断保留旧快照，替换后中断使用新快照。同目录遗留的 `.strata-*` 临时文件不会参与恢复，可在服务停止时清理。若替换后同步目录失败，服务保留新内存状态并停止后续写入，健康状态变为 503；检查磁盘并重启后再读取版本确认结果，不要盲目重放修改。

该存储方案针对小规模资料，使用整份快照和内存副本，读写开销随历史体积增长。未实现数据删除、自动清理历史、备份轮替或多进程共享。备份时停止服务后复制数据目录；不要人工修改快照内容。

## 构建与验证

```bash
go build ./...
go vet ./...
python3 scripts/smoke.py all
python3 scripts/smoke.py exchange
STRATA_SMOKE_RACE=1 python3 scripts/smoke.py seal
```

**测试模式为 `deferred`**：当前初始化基线有意不生成单元测试、测试数据或专用测试套件；后续“代码测试”任务补充这些内容。`scripts/smoke.py` 是有超时的运行验证入口，它在临时目录编译并启动真实 HTTP 服务、通过本机回环 HTTP 连接完成操作，然后关闭服务并清理临时数据。不会访问外网或修改现有数据目录。也可分别运行 `record / seal / compare / browse / exchange` 五个流程。

验证覆盖编录成功与深度失败边界、锁定与重新打开、并发版本冲突、历史不变性、数据目录独占、重启恢复、区间相似度、未知岩性、偏移建议、CSV、列表筛选与分页、交换包闭包、确定性摘要、不可变性、缺失引用区分和无法识别格式的明确无效结论。

## 目录

```text
cmd/stratad/          启动、信号和 HTTP 服务生命周期
internal/api/        JSON、路由、查询参数和请求记录
internal/catalog/    编录、修订、查阅和对比工作流
internal/geology/    分层规则、覆盖、深度查询和版本差异
internal/correlation/区间对比、标志层偏移与 CSV
internal/exchange/  资料交换包闭包、摘要、不可变校验和只读检查
internal/persistence/快照校验、原子替换、独占锁
internal/config/     环境变量与启动参数
scripts/smoke.py      临时环境 HTTP 运行验证
```
