# 最近一次测试报告

2026-10-17 22:45:12 CST：按 `CODE_REVIEW.md`（2026-10-17）修复 C01–C15 后的开发验收。测试对象是未提交的工作区（基线 `564e73b`），最后一轮在全部改动完成后整体重跑；未提交、未推送、未部署生产。

## 测试环境

- netcup Debian 13，新建隔离目录（工作区打包复制，0 个 `._*` 文件），不连接生产数据库、照片、RustFS 或真实 AI 服务；生产容器全程没有改动或重启（healthy）。
- Go：`golang:1.27-alpine`（`-tags nodynamic`，race 另装 build-base）；前端与浏览器：`mcr.microsoft.com/playwright:v1.63.0-noble`（Node、Chromium、WebKit，Next.js 16.3.7）。
- 测试服务 `output/bijin-ui-test`（127.0.0.1:18092 / 18094）、`tests/s3_fixture.py`（18093）、`tests/recognizer_fixture.py`（18095），测试图片由 `genphotos.go` 新建。

## 测试方法与结果（最后一轮，最终工作区）

| 编号 | 方法 | 结果 |
| --- | --- | --- |
| T01 | `npm ci && npm run typecheck && npm run build`；`tsc --noEmit --noUnusedLocals --noUnusedParameters` | 通过；未使用检查 0 项 |
| T02 | `npm run test:organizer / test:upload / test:names / test:recognition` | 15 / 21 / 3 / 6 通过，0 失败 |
| T03 | `gofmt -l`、`go vet`、`staticcheck`（2026.2.2）、`deadcode` | 全部无输出 |
| T04 | `go test -tags nodynamic -count=1 -json .` | 179 个顶层测试通过、0 失败；`TestQueryScale` 可选压测按默认跳过 |
| T05 | 同上加 `-race` | 179 通过，0 处 DATA RACE |
| T06 | 干净数据上全量 `npx playwright test` | Chromium / WebKit 共 125 项通过，0 失败，6.5 分钟 |

新增的回归（均在 T04–T06 中通过）：

- Go：`TestRecognizerKeepsKeyOnlyAtTheSameAddress`、`TestSharingVisitorsDoNotTakeOwnerSlots`、`TestBatchSharingRunningDetailsCarryFailuresOnly`。
- 浏览器（两种浏览器各一次）：「a refused confirmation shows its reason inside the dialog…」「the shared-only gallery waits for a running task to end, then reloads once」；`recognition 01` 增加换接口地址不发送旧 Key 的检查。

## 业务验收

| 编号 | 用户想做什么 / 预期 | 证据 | 状态 |
| --- | --- | --- | --- |
| C01 | 换「其他兼容服务」的地址时，旧 Key 不发给新地址：留空保存或测试返回「换了接口地址，请重新填写 API Key」，新地址收不到请求；同一地址写法不同（空格、末尾 /）仍沿用 | Go 与 recognition 01 | 通过 |
| C02 | 批量确认被拒绝（模拟 409）时，原因显示在弹窗里；「返回」后清除；再次开始能完成 | 浏览器新回归 | 通过 |
| C03 | 「只看已分享」打开时，批量任务处理中（模拟 3 次进度）不重新读取列表，结束时只读取 1 次并显示空态 | 浏览器新回归；原有停止分享回归 | 通过 |
| C04 | 处理中的任务详情只带失败项，结束后带全部；超过 48 小时的记录按不存在处理，许可不受影响 | Go 新回归；`TestBatchSharingGateExpiryReplacementAndSummary` | 通过 |
| C05 | 访客占满公开名额时，站长 100 ms 内拿到自己的名额 | Go 新回归 | 通过 |
| C06–C15 | 删除无效代码、拆分文件与文档整理后，原有功能不变 | T01–T06 | 通过 |

## 失败、修复与复测记录

- 第一轮 Go 全量有 1 项失败：`TestBatchSharingGateExpiryReplacementAndSummary`。原因是详情接口去掉清理后，超过 48 小时的记录仍能读到，属于实现回退。修复实现（读取时过期记录按不存在处理），测试没有改；之后全部重跑通过。
- `gofmt` 指出新加测试前缺一个空行，已改。
- 没有删除或放宽任何原有测试。原有测试只做两类调整，都跟随内部接口变化：`photoPage` 多了「只看已分享」参数；读取任务改用 `store.readBatch`；名额测试改用新的 `slots`。

## 限制与结论

- 「只看已分享」的刷新次数通过拦截响应模拟进度来验证，没有在真实的长任务上测量；没有在 iPhone 真机上测试。
- 测试环境（隔离目录、Go 缓存卷、Go 与 Playwright 镜像、构建缓存）已删除，netcup 只剩 bijin / RustFS / OpenResty。生产服务器仍没有 bijin 数据库备份（10-17 19:08 清理时按用户要求删除）。
- 结论：C01–C15 已按优先级修复，最终工作区的构建、静态检查、Go（含 race）与两种浏览器回归全部通过；尚未提交、推送或部署。
