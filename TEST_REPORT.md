# 最近一次测试报告

2026-10-17 13:43:49 CST：整理页识别建议（外部识别服务给出作者 / 模特建议）。基线 `7a56378`；方案见 `docs/album-recognition-plan.md`（Q1–Q21 由用户确认）。本次只在隔离环境验收，未部署生产、未推送 GitHub。

## 测试环境

- netcup Debian 13，隔离目录 `/root/bijin-recognize-test/src`（本地工作区副本），不连接生产数据库、照片、RustFS 或任何真实 AI 服务。
- Go：`golang:1.27-alpine`，`-tags nodynamic`，新增依赖 `github.com/anthropics/anthropic-sdk-go v1.82.0`；前端与浏览器：`mcr.microsoft.com/playwright:v1.63.0-noble`（Node、Chromium、WebKit），Next.js 16.3.7。
- 测试服务为同一份代码构建的 `output/bijin-ui-test`（127.0.0.1:18092 / 18094），配合 `tests/s3_fixture.py`（18093）和新增的 `tests/recognizer_fixture.py`（18095，OpenAI 格式假识别服务，按模型名正常 / 延迟 3 秒 / 回答非 JSON，并记录收到的请求）。Go 测试用 `httptest` 假 OpenAI 格式服务和假 Anthropic Messages API 服务。
- 最终全量前删除测试图片与数据库，由 `genphotos.go` 重建。

## 方法与结果

| 编号 | 实际方法 | 结果 |
| --- | --- | --- |
| T01 | `npm ci && npm run typecheck && npm run build` | 通过，静态导出完成 |
| T02 | `npm run test:organizer` / `test:upload` / `test:names` / `test:recognition` | 15 / 21 / 3 / 5 项通过，0 失败 |
| T03 | `go vet` 后 `go test -tags nodynamic -count=1 -json .` | 176 个顶层测试通过（含新增识别 8 项），0 失败；`TestQueryScale` 可选压测按默认跳过 |
| T04 | `CGO_ENABLED=1 go test -race`（同上） | 176 通过、1 跳过，无 DATA RACE |
| T05 | 干净数据下 `npx playwright test`（全量） | Chromium / WebKit 共 102 项通过，0 失败，5.0 分钟 |
| T06 | `npx playwright test tests/logout.spec.ts --repeat-each=10` | 两浏览器共 120 项通过，0 失败 |
| T07 | 取回截图人工复核（设置页卡片桌面 / 320 px，整理页建议桌面 / 320 px） | 卡片与对象存储卡片同一风格；建议为虚线框，名字整词显示，「新名字」放不下时整体换行；320 px 无横向溢出 |
| T08 | 本地工作区 141 个非文档文件与隔离副本逐个 SHA-256 比对 | 全部一致，测试的就是本地代码 |

## 业务验收

| 编号 | 用户想做什么 | 操作与预期 | 证据 | 状态 |
| --- | --- | --- | --- | --- |
| R01 | 配置识别服务 | 设置页选服务、填地址 / Key / 模型 / 推理强度后保存；显示服务、模型、推理强度、「已保存」；接口与设置页数据都不含 Key | `recognition.spec.ts` 01；`TestRecognizerAPIGatesKeyAndConfig` | 通过 |
| R02 | 测试不保存 | 点「测试」显示「测试成功」，配置仍为未配置；回答不是 JSON 时显示原因 | spec 01；`TestRecognizerTestDoesNotSave` | 通过 |
| R03 | Key 留空沿用 / 换服务重填 | 同服务留空沿用原 Key；换到 Grok 留空，页面与接口都要求重填，原配置不变 | spec 01；Go 同上 | 通过 |
| R04 | 未配置不可识别 | 整理页按钮不可点，提示并链接到设置页；接口返回 409 | spec 03；Go 同上 | 通过 |
| R05 | 识别给出建议 | 只为空项给建议；已填作者的相册不出现作者建议；根目录不识别；接受前资料不变 | spec 02；`TestRecognizeOpenAIFormatChecksEverySuggestion` | 通过 |
| R06 | 接受 / 忽略 / 全部接受 | ✓ 保存并显示「已保存」；✕ 消失且不保存；「接受全部建议」只作用于当前列表，名单出现新名字；手动填写后建议隐藏 | spec 02 | 通过 |
| R07 | 名单写法与新名字 | 大小写不同的已有名字改用名单写法，未在名单的标「新名字」 | spec 02；Go 同上 | 通过 |
| R08 | 不合格回答 | 未请求的相册、已填项、重复相册、重复模特被跳过并计数；非 JSON / 缺 albums 整批失败 | `TestRecognizeOpenAIFormatChecksEverySuggestion`、`TestRecognizeReportsServiceFailures`、`TestParseRecognition` | 通过 |
| R09 | 分批与进度 | 每 20 本一批、最多 100 本、顺序请求；进度「识别中 0 / N」；单批失败显示「第 1 批（N 本）：原因」，资料不变 | `recognition.test.mjs`；spec 04 | 通过 |
| R10 | 超时与错误 | 401 / 404 / 429 / 503、拒绝回答、截断、空回答、连不上各有中文原因；超过期限返回 504；资料不变 | `TestRecognizeReportsServiceFailures`（期限缩短为 0.3 秒） | 通过 |
| R11 | Claude 原生请求 | `/v1/messages`、`x-api-key`、`max_tokens` 16000、`output_config.format` 为 JSON Schema、推理强度映射到 `effort`、默认时不发、不发 temperature / thinking；拒绝与 401 显示原因 | `TestRecognizeClaudeUsesMessagesAPI` | 通过 |
| R12 | OpenAI 格式请求 | `/chat/completions`、Bearer Key、JSON Schema、`reasoning_effort`、不发 temperature / max_tokens；400 提到 response_format 时去掉重试一次，并能读带代码块的 JSON | `TestRecognizeOpenAIFormatChecksEverySuggestion`、`TestRecognizeRetriesWithoutSchemaAndReadsFencedJSON` | 通过 |
| R13 | 门禁 | 五个接口未登录 401，跨站 403，非 JSON 415 | `TestRecognizerAPIGatesKeyAndConfig` | 通过 |
| R14 | 回归 | 全量 Go / race、前端模块、浏览器全量与退出登录重复 | T02–T06 | 通过 |
| R15 | 真实服务 | 用户在设置页填入一家真实服务的 Key，测试并识别真实相册 | 需要用户提供 Key | 未测试 |

## 失败、修复与复测记录

- Go 首次：2 项失败。① 已填满但被请求的相册被排除出示例，属实现错误，改为只把真正要识别的相册排除；② 测试里错误原因的预期文字写错（漏了「请在设置页检查」），改正预期。复测 8 项通过。
- 浏览器首次（只跑识别）：6 项失败。① 320 px 时直接改窗口宽度，侧栏仍展开遮住按钮，改为改宽度后重新加载（手机打开页面时侧栏默认收起）；② 预期数字写错（第 04 项应为 5 条建议）；③「手动填写后建议隐藏」一步中，选完模特后按 Esc，模特被清空。另用探针在未配置识别服务的整理页复现：选模特后按 Esc，会发送 `models: []` 并保存为空。这是整理页原有的行为，与本功能无关，测试按原有整理测试的做法不再按 Esc，问题单独报告。
- 截图复核：带「新名字」标记的名字被挤成一个字母一行（`overflow-wrap: anywhere` 使最小宽度过小），改为 `break-word` 并让名字按文字排版，加「、」分隔。
- 第一次全量：99 项通过、3 项失败（均为 WebKit）。① 识别 02、04 写死「3 本」，而前面的上传测试留下了 12 本相册，改为从接口计算数量；② 退出登录 04：设置页另外请求 `/api/recognizer`，一进设置页就退出时请求被页面跳转中断，WebKit 记为页面错误（单独重复 15 次中 14 次出现）。改为识别服务状态随 `/api/settings` 一起返回，设置页不再单独请求；复测重复 15 次全部通过。
- 最终全量（T05、T06）全部通过，没有修改断言迎合实现、没有删除失败测试。

## 限制与结论

- 未用真实 AI 服务测试（R15）：需要用户的 API Key。四家服务的请求格式按官方文档实现，并由假服务核对请求内容；Gemini 兼容接口对 JSON Schema 的实际支持、各模型对推理强度的支持，需要用真实 Key 在设置页「测试」确认。
- 未开启 `TestQueryScale` 10 万 / 20 万条可选压测；本次未改照片查询。
- 全部测试在隔离目录进行，没有读写生产数据；为设计方案只读取过生产数据库副本（读完即删）和照片文件头。
- 结论：功能与回归测试通过；尚未部署生产和推送，等待用户授权。另发现整理页原有问题：在相册行的模特下拉里按 Esc 会清空这本相册的全部模特并保存，未在本次修改。
