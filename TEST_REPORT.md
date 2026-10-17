# 最近一次测试报告

2026-10-17 14:45:31 CST：合并验收、推送与生产部署（发布前验收完成于 14:40:45）。合并提交 `2c589db` 包含两部分：整理页识别建议 `a345e41`（方案见 `docs/album-recognition-plan.md`），以及名字下拉按 Esc 不再清空已选名字 `1bb7b56`。基线 `7a56378`。用户已授权：合并后在隔离环境重跑全量测试，推送 master，部署生产，清理测试与旧镜像。

## 测试环境

- netcup Debian 13，隔离目录 `/root/bijin-release-test/src`，内容为 `git archive 2c589db`（158 个文件），不连接生产数据库、照片、RustFS 或任何真实 AI 服务。
- Go：`golang:1.27-alpine`，`-tags nodynamic`，新增依赖 `github.com/anthropics/anthropic-sdk-go v1.82.0`；前端与浏览器：`mcr.microsoft.com/playwright:v1.63.0-noble`（Node、Chromium、WebKit），Next.js 16.3.7，`@base-ui/react` 1.8.0。
- 测试服务为同一份代码构建的 `output/bijin-ui-test`（127.0.0.1:18092 / 18094），配合 `tests/s3_fixture.py`（18093）和 `tests/recognizer_fixture.py`（18095，OpenAI 格式假识别服务）。Go 测试用 `httptest` 假 OpenAI 格式服务与假 Anthropic Messages API。测试图片由 `genphotos.go` 新建。

## 方法与结果（合并提交 `2c589db`）

| 编号 | 实际方法 | 结果 |
| --- | --- | --- |
| T01 | `npm ci && npm run typecheck && npm run build` | 通过，静态导出完成 |
| T02 | `npm run test:organizer` / `test:upload` / `test:names` / `test:recognition` | 15 / 21 / 3 / 5 项通过，0 失败 |
| T03 | `go vet` 后 `go test -tags nodynamic -count=1 -json .` | 176 个顶层测试通过，0 失败；`TestQueryScale` 可选压测按默认跳过 |
| T04 | `CGO_ENABLED=1 go test -race`（同上） | 176 通过、1 跳过，无 DATA RACE |
| T05 | 干净数据下 `npx playwright test`（全量） | Chromium / WebKit 共 104 项通过，0 失败，5.2 分钟（原 94 项 + 识别 8 项 + Esc 2 项） |
| T06 | `npx playwright test tests/logout.spec.ts --repeat-each=10` | 两浏览器共 120 项通过，0 失败，5.3 分钟 |
| T07 | 截图人工复核（识别服务卡片、整理页建议，桌面与 320 px） | 与现有卡片同一风格；建议为虚线框，名字整词显示，「新名字」放不下时整体换行；无横向溢出 |
| T08 | 推送：`git push origin master`（`93543c8` 只比 `2c589db` 多文档，代码逐文件一致） | GitHub master 为 `93543c8` |
| T09 | 部署前：SQLite 在线备份到 `/root/bijin-backup-20261017-predeploy.db`（0600）并 `quick_check`，记录各表行数；当前镜像打回滚标签 | quick_check ok；photos 4288、photo_shares 4288、album_people 92、people 27、wallpapers 4333、storages 1、share_batches 3、share_batch_items 455、organize_receipts 11 |
| T10 | 生产 `git pull --ff-only` 到 `93543c8`，`docker compose build && up -d` | 新镜像 `9b4e2fd4b4c3`，启动后 healthy、重启 0 次；启动扫描 4288 张、0 失败；日志只有 INFO；镜像内含「识别未填写的相册」与 Esc 修复 |
| T11 | 部署后数据库核对 | quick_check ok；原有各表行数与部署前一致；新表 `recognizer` 已建、为空；数据库仍为 0600 |
| T12 | 公网核对 `https://csb.jgbman.cc` | health 200、登录页 200；未登录首页 302 到登录，`/api/recognizer`、`/api/recognize`、`/api/settings` 401；随机壁纸 302；现有 503 个分享许可中抽查一个，HEAD 200 image/jpeg |
| T13 | 清理 | 删除两个隔离目录、Playwright / Go / Node 测试镜像、3 个测试卷、回滚镜像（旧 `060e834b42e3`）与构建缓存，回收约 9.8 GB；netcup 只剩 bijin / RustFS / OpenResty；部署前数据库备份保留 |

## 业务验收

| 编号 | 用户想做什么 | 操作与预期 | 证据 | 状态 |
| --- | --- | --- | --- | --- |
| R01 | 配置识别服务 | 设置页选服务、填地址 / Key / 模型 / 推理强度后保存；显示服务、模型、推理强度、「已保存」；接口与设置页数据都不含 Key | `recognition.spec.ts` 01；`TestRecognizerAPIGatesKeyAndConfig` | 通过 |
| R02 | 测试不保存 | 「测试」成功后仍为未配置；回答不是 JSON 时显示原因 | spec 01；`TestRecognizerTestDoesNotSave` | 通过 |
| R03 | Key 留空沿用 / 换服务重填 | 同服务留空沿用；换到 Grok 留空，页面与接口都要求重填，原配置不变 | spec 01；Go 同上 | 通过 |
| R04 | 未配置不可识别 | 整理页按钮不可点并链接设置页；接口 409 | spec 03；Go 同上 | 通过 |
| R05 | 识别给出建议 | 只为空项给建议；根目录不识别；接受前资料不变 | spec 02；`TestRecognizeOpenAIFormatChecksEverySuggestion` | 通过 |
| R06 | 接受 / 忽略 / 全部接受 | ✓ 保存；✕ 不保存；「接受全部建议」只作用于当前列表；手动填写后建议隐藏 | spec 02 | 通过 |
| R07 | 名单写法与新名字 | 大小写不同的已有名字改用名单写法，名单外标「新名字」 | spec 02；Go 同上 | 通过 |
| R08 | 不合格回答 | 未请求 / 已填 / 重复条目跳过并计数；非 JSON 整批失败 | Go 识别测试与 `TestParseRecognition` | 通过 |
| R09 | 分批与进度 | 每 20 本一批、最多 100 本；「识别中 0 / N」；单批失败显示原因，资料不变 | `recognition.test.mjs`；spec 04 | 通过 |
| R10 | 超时与错误 | 401 / 404 / 429 / 503、拒绝、截断、空回答、连不上各有中文原因；超时 504 | `TestRecognizeReportsServiceFailures` | 通过 |
| R11 | Claude 原生请求 | `/v1/messages`、JSON Schema 约束、推理强度映射 `effort`、不发 temperature / thinking | `TestRecognizeClaudeUsesMessagesAPI` | 通过 |
| R12 | OpenAI 格式请求 | JSON Schema、`reasoning_effort`、不发 temperature；不支持 JSON Schema 时去掉重试一次 | Go 识别测试 | 通过 |
| R13 | 门禁 | 五个接口未登录 401、跨站 403、非 JSON 415 | `TestRecognizerAPIGatesKeyAndConfig` | 通过 |
| R15 | 真实服务 | 在设置页填入一家真实服务的 Key，测试并识别真实相册 | 需要用户的 Key | 未测试 |
| E01 | 新增模特后按 Esc 不丢失 | 新增「Probe Name」→ 已保存 → Esc：模特仍在，不发新的保存 | `organize-saving.spec.ts` saving 07，两浏览器 | 通过 |
| E02 | 作者框按 Esc 不丢失 | 选定「Studio A」后多次 Esc，作者仍在，不保存空作者 | saving 07 | 通过 |
| E03 | Esc 取消正在输入的文字 | 输入「Draft」后 Esc 关闭下拉、再 Esc 清掉文字，已选名字不变 | saving 07 | 通过 |
| E04 | 没有多余写入 | Esc 期间 `POST /api/organize` 只有选名字的两次；刷新后服务器资料不变 | saving 07 网络记录与 `/api/albums` | 通过 |
| E05 | 筛选条件按 Esc 不清空 | 模特筛选选中后按两次 Esc，筛选仍生效 | saving 07 | 通过 |
| R14 / E06 | 回归 | 全量 Go / race、前端模块、浏览器全量与退出登录重复 | T02–T06 | 通过 |

## 失败、修复与复测记录

- 识别建议（合并前，`/root/bijin-recognize-test`）：Go 首次 2 项失败（已填满的被请求相册被排除出示例，属实现错误已修；一处预期文字写错已改正）。浏览器首次 6 项失败（320 px 改宽度后需重新加载、两处预期数字写错，以及选模特后按 Esc 被清空——即 E01 的原有问题）。截图发现「新名字」使名字被挤成一个字母一行，改为整词排版。第一次全量 3 项 WebKit 失败：识别用例写死「3 本」而上传测试留下了 12 本相册，改为从接口计数；设置页单独请求 `/api/recognizer` 在立刻退出时被页面跳转中断（重复 15 次 14 次出现），改为随 `/api/settings` 返回，复测 15 次全部通过。
- Esc 修复（另一会话，`/root/bijin-esc-test`）：未修改代码上 saving 07 两浏览器失败作为复现证据，修复后通过；一处断言依赖残留上传图片的总本数，改为不依赖总数后在干净数据上重跑通过。该会话已清理其隔离目录。
- 合并后（本次 T01–T06）一次通过。没有修改断言迎合实现、没有删除失败测试。

## 限制与结论

- 未用真实 AI 服务测试（R15）：需要用户的 API Key。Gemini 兼容接口对 JSON Schema 的实际支持、各模型对推理强度的支持，需在设置页「测试」确认。
- 未开启 `TestQueryScale` 10 万 / 20 万条可选压测；本次未改照片查询。
- 测试全部在隔离目录进行；为设计方案只读取过生产数据库副本（读完即删）和照片文件头。
- 结论：合并提交 `2c589db` 的功能与回归测试全部通过；`93543c8` 已推送并部署生产，生产 healthy，原有数据、配置与分享保留。识别服务需用户在设置页填入真实 Key 后才能使用和验收（R15）。
