# 最近一次测试报告

2026-10-17 14:19:51 CST：修复名字下拉按 Esc 清空已保存的作者 / 模特。基线 master `7a56378`，只改 `frontend/components/person-picker.tsx` 与浏览器用例；未部署、未推送。

## 测试环境

- netcup Debian 13，隔离目录 `/root/bijin-esc-test`（本工作树已跟踪文件的副本），不连接生产数据库、照片或 RustFS，未触碰 `/opt/1panel/apps/web_bijin`。
- 前端：`node:24-alpine`，Next.js 16.3.7，`@base-ui/react` 1.8.0；Go：`golang:1.27-alpine`，`-tags nodynamic`；浏览器：`mcr.microsoft.com/playwright:v1.63.0-noble`，Chromium / WebKit。
- 测试服务为同一份代码构建的 `output/bijin-ui-test`（127.0.0.1:18092 / 18094）与 `tests/s3_fixture.py`（18093），均在 Playwright 容器自己的网络里运行，不占用宿主机端口。每次全量前删除测试图片与数据库，由 `genphotos.go` 重建 58 张有效图片（含一个故意损坏文件）。

## 方法与结果

| 编号 | 实际方法 | 结果 |
| --- | --- | --- |
| T01 | 未修改代码上运行新用例 `organize-saving.spec.ts` 「saving 07」 | 两浏览器均失败：点「添加「Probe Name」」显示已保存后按一次 Esc，模特变为 `[]` |
| T02 | 未修改代码上临时探针（已删除，未提交）：作者选「Studio A」后按 Esc | Chromium 发送 `{"author":""}`，服务器作者变空；WebKit 第一次 Esc 只关闭仍开着的下拉 |
| T03 | 未修改代码上临时探针：模特筛选选中后按两次 Esc | 两浏览器筛选条件都被清空，列表恢复「共 4 本」 |
| T04 | 修复后 `npm run typecheck && npm run build` | 通过 |
| T05 | `npm run test:organizer` / `test:upload` / `test:names` | 15 / 21 / 3 项通过，0 失败 |
| T06 | `go vet`、`gofmt -l`、`go test -tags nodynamic -count=1 -json .` | vet / 格式无问题；168 个顶层测试通过，0 失败；`TestQueryScale` 可选压测按默认跳过 |
| T07 | 干净数据下 `npx playwright test`（全量，最终版用例） | Chromium 57 / WebKit 39，共 96 项通过，0 失败、0 不稳定，4.7 分钟；修复前基线为 94 项 |
| T08 | 本工作树与 netcup 副本的改动文件 SHA-256 比对 | `person-picker.tsx`、`organize-saving.spec.ts` 一致 |

## 业务验收

| 编号 | 用户想做什么 | 操作与预期 | 证据 | 状态 |
| --- | --- | --- | --- | --- |
| E01 | 新增模特后按 Esc 不丢失 | 模特框新增「Probe Name」→ 已保存 → Esc：模特仍在，不发送新的保存 | saving 07，两浏览器 | 通过 |
| E02 | 作者框按 Esc 不丢失 | 回车选定「Studio A」后多次 Esc：输入框仍显示「Studio A」，不保存空作者 | saving 07 | 通过 |
| E03 | Esc 取消正在输入的文字 | 模特框、作者框各输入「Draft」→ Esc 关闭下拉 → 再 Esc：模特框为空、作者框恢复「Studio A」，已选名字不变 | saving 07 | 通过 |
| E04 | 没有多余写入 | 上述 Esc 期间 `POST /api/organize` 只有选名字的两次（`["Probe Name"]`、`"Studio A"`）；刷新后服务器资料不变 | saving 07 网络记录与 `/api/albums` | 通过 |
| E05 | 筛选条件按 Esc 不清空 | 整理页模特筛选选中后按两次 Esc：筛选仍是「Probe Name」，仍只显示 1 本 | saving 07 | 通过 |
| E06 | 原有键盘、删除、清除与筛选操作不变 | Backspace 删模特、✕ / 清除按钮、筛选后 Esc 关闭下拉等原有用例 | 全量 96 项 | 通过 |

## 失败、修复与复测记录

- 第一次运行（未修改代码）：saving 07 两浏览器在报告的那一步失败，作为复现证据；修复后两浏览器通过。
- 用例补充筛选检查后，在上一轮全量留下的上传图片上单独运行，探针的「显示 1 / 4 本」断言因残留图片（共 22 本）不符；与 Esc 无关。重建测试图片后复测，并把用例改为只核对「显示 1 / …」，不依赖总本数。
- 最终在干净数据上完整重跑全量 96 项通过。

## 限制与结论

- 未开启 `TestQueryScale` 可选压测；本次只改前端名字下拉，接口与数据库结构未变。
- 未在生产或真机手机上验证；未部署。合并回 master、部署需用户同意。
- 结论：Esc 不再清空已选作者 / 模特或筛选条件，只关闭下拉或取消正在输入的文字；前端模块、Go 全量与两浏览器全量回归通过。
