# 最近一次测试记录

2026-10-10 17:47:49 CST：整理相册保存与上传批次 module。实现基线 `a9f040a`；隔离验证、生产构建、部署及公网浏览器验收完成；推送后清理临时验收资料。

## 环境

- 本地 `/root/workspaces/web-bijin-organize`：Linux amd64、Go 1.27.2、Node 26.7.0、锁定依赖；Go 使用 `-tags nodynamic`、`GOMAXPROCS=3`、`-p 2` 和独立磁盘临时目录。
- netcup 隔离副本 `/root/bijin-dev-upload`：Playwright 1.63.0 镜像、Node 24.20.0、Chromium / WebKit；测试与构建限 3 核 / 4 GB，浏览器共享内存 512 MiB。
- 58 张生成图片、独立 SQLite 和仅监听测试容器本机的 S3 HTTP fixture；完整回归前重置数据。浏览器访问实际 Go 内嵌的静态导出页面。

## 方法与结果

| 编号 | 功能与实际操作 | 预期与验证方式 | 结果及证据 |
|---|---|---|---|
| O01 | 连续选模特 A、B、C，延迟真实保存响应 | 最新预览不回退，所有相关操作确认后才显示已保存；module interface、Chromium / WebKit、真实 SQLite | 通过；`saving 01`，15 项整理 module 测试 |
| O02 | 批量、单行、改名 / 合并 / 删除交错；新名字确认时继续编辑 | 遵循同一操作顺序，后续引用更新身份，输入不丢失；真实请求顺序及最终关联 | 通过；`saving 02`、原整理测试、Go / module 行为测试 |
| O03 | 已提交后响应丢失、请求未送达、拒绝长名字、回执过期 / 裁剪 | 按编号核对 / 重试一次；待确认阻止后续发送；拒绝保留较新修改；过期不重做 | 通过；`saving 03–05`、SQLite 重开 / 并发 / 回滚及受控 adapter |
| O04 | 慢回执响应、旧读取晚到、待保存时切页 / 关闭 / 退出 | 回执响应释放 SQLite 连接，旧响应不覆盖新资料，离开前提醒 | 通过；Go 阻塞响应、module、`saving 06` 双浏览器 |
| U01 | 本地单张 / 多张、文件夹 / 拖入、S3 直传 | 进入相册、缩略图可读、原图字节一致，同名不覆盖 | 通过；原 `upload 01–06`，真实 Go / HTTP S3 fixture |
| U02 | 本地 PUT 已保存但丢失响应 | 原任务核对后完成，prepare / PUT / verify / complete 为 1 / 1 / 1 / 0，无重新准备 | 通过；`recovery 01` 双浏览器及 module，磁盘原图字节一致 |
| U03 | S3 PUT 或 complete 响应丢失 | 核对原对象任务标记和大小；prepare / PUT / verify 各 1，已保存原图不重传 | 通过；两项 S3 recovery 双浏览器、Go verify，原图字节一致 |
| U04 | 断网核对失败，另一张继续；手动继续确认仍失败 | 自动只核对一次，手动每次只核对一次，待确认不阻塞独立图片 | 通过；`recovery 04` 双浏览器、module 请求次数断言 |
| U05 | 五张任务同时开始，停止四张活动任务；旧回调 / 目录读取晚到 | 立即取消，不发送第五张；结果未知保留，旧结果不能填入新批次；运行中固定位置 / 名单 | 通过；`recovery 05`、module 取消 / 位置 / 清空行为 |
| U06 | 已保存但处理等待两分钟；明确处理失败 / 坏图；任务过期 | 不持续查询或自动重试，不重传已保存原图；手动续查 / 处理重试；失效保留 saved 事实 | 通过；受控时钟 / 响应 module、两项 expired 双浏览器、Go 坏图 / 传输恢复 |
| U07 | 未上传 / 待确认时切页或退出；上传中留下 / 确认退出 | 提醒覆盖未完成项；留下保持上传，确认离开不再发 verify 或下一项 | 通过；`recovery 04–06`、`logout 05` 双浏览器、module hold / stop |
| U08 | verify 的未登录、跨来源、非 JSON、失效、S3 查询故障 | 拒绝非法访问；404 明确失效；存储故障不误报原图不存在 | 通过；7 项新增 Go 顶层验收及既有输入检查 |
| R01 | 原照片 / 相册 / 大图、公开壁纸、登录 / 两步验证 / 退出、整理及上传 | 原有功能保持；完整回归与静态构建 | 通过；43 Chromium + 26 WebKit，共 69 项 |

实测统计：

- `npm run test:organizer`：15 / 15；`npm run test:upload`：21 / 21，本地及目标 Node 24 均通过。只通过 module 的 interface 观察结果与 adapter 请求。
- `go test -tags nodynamic -p 2 -json -count=1 .`：144 个顶层测试通过，0 失败，52.477 秒；1 个既有可选 `TestQueryScale` 未显式启用。
- `go test -race -tags nodynamic -p 2 -json -count=1 .`：144 通过，0 失败，同一可选测试跳过，111.539 秒。
- `go vet -tags nodynamic .`、gofmt、`git diff --check`、类型检查、Node 24 静态导出、`CGO_ENABLED=0 go build -tags nodynamic -trimpath`：通过。
- 最终 `npx playwright test`：69 通过、0 失败、0 跳过、0 flaky，214.765 秒；此前完整回归亦 69 通过。日志：`output/browser-test-final.log`、`output/frontend-final.log`、`output/upload-ui-results.json`；Go JSON：`output/upload-go-test.json`、`output/upload-go-race.json`。

## 修复与测试边界

- 首轮浏览器回归为 63 通过、5 失败、1 项受串行失败阻断：四项失败来自把「查看相册」渲染控件错误定位成 link；改用其真实锚点定位。另一项是 WebKit 确认退出期间，传输中断触发核对，产生页面异常。保留页面异常断言，按生命周期暂缓新请求，加入「退出不再核对」及 hold / stop 行为验收后完整重跑通过。
- 手动继续确认断网时最初会再次自动核对；增加有界确认行为及独立验收，最终版本重建并重新跑全部 69 项。
- 浏览器响应丢失验收将选中的同一份 fixture 字节转发给真实 Go PUT 后丢弃回答；S3 complete 丢失先执行真实请求再断开。它验证保存与恢复事实，不冒充真实断网设备测试。已有旧版透明代理探针亦复现 HTTP 200 正文中断后的错误行为。
- 本地 WebKit 缺少动态库，改用 netcup 既有 Playwright 镜像完成测试，未跳过 WebKit。Node 模块推断与颜色环境警告不影响验收；未发现未预期页面异常。

## 结论

约定的整理顺序 / 最新预览 / SQLite 短期回执，以及上传批次 / 四路执行 / 有界核对 / 停止 / 手动处理恢复 / 过期 / 离开提醒均已实现并有实际证据。上传任务仍在进程内，应用重启或过期后不会自动重建；确认离开后浏览器文件列表不保留。未在实体 Safari / iPhone 设备测试。生产部署及公网验收通过；临时验收资料将在推送后定向清理。


## 生产部署与验收（2026-10-10 17:52:07 CST）

- 按用户要求部署生产：新镜像 `sha256:2fdec186259ccc7aee415c0468722488f0ec4f8bf57c5f445b4bc9d43caba986`，`docker compose up -d --no-build bijin` 后 healthy；容器仍为 6 核 / 4 GiB，原挂载与端口保持。部署前生成的短时管理员验收会话在新容器继续有效，没有修改登录或两步验证配置。
- 部署启动扫描：3,997 张照片，0 失败、3,997 ready；原五张业务表内容哈希完全相同（photos 3,997、wallpapers 4,042、people 23、album_people 80、storages 1），会话密钥、`.env` 和 Compose 哈希一致。新增整理回执表不改变原资料。
- Chromium 通过公网 `https://csb.jgbman.cc` 验收：未登录私有接口返回 401；公开随机壁纸跳转和成品 SHA-256 与 URL 一致。
- 独立临时目录下，本地 PUT 已保存后丢失响应、S3 complete 已保存后丢失响应均恢复完成；两者 prepare / PUT / verify 各 1，complete 分别 0 / 1，任务 saved=true、done，相册原图字节与选中文件逐字节一致，没有重新准备或重传。
- 同一临时相册连续填写两个模特，暂扣真实 SQLite 回答；旧回答不回退最新选择，后续修改确认前保持保存中，最终两个选择均保存，原编号回执 committed；两次按序写入，页面异常 0。
- 生产只增加两个有清单的临时原图和两个临时名字。当前推送完成后定向清理原图、人物、关联与操作回执，再核对业务表和配置；不按照片名称猜测或批量删除已有资料。
- 证据：`output/production-smoke-result.json`、`output/production-data-check.json`、`output/deploy-build.log`。生产验收不冒充实体 Safari 测试。
