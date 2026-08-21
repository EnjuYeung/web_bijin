# Design — Juen's

这是 Juen's 本地相册的锁定设计系统。主图库、登录页和大图层都必须先读取本文件，再调整视觉样式；业务逻辑、路由和数据结构不属于本文件范围。

## 使用场景

- 受众：家庭服务器的单一使用者。
- 核心任务：快速浏览照片、进入文件夹相册、打开原图并查看信息。
- 语气：梦幻、轻微二次元、锐利、安静；照片永远比装饰更重要。

## Genre

`atmospheric`。画布允许两处静态光晕，但不使用粒子、飘落花瓣、渐变文字、玻璃拟态或持续动画。

## Macrostructure family

- 主图库：`Portfolio Grid`，真实照片是主要内容，标题和工具保持克制。
- 登录页：`Photographic`，随机照片全幅展示，登录表单作为左下角的功能面板。
- 大图层：功能型影像舞台，信息面板不套卡片，不增加营销式结构。
- 导航：`N3 Side-rail` 的应用化变体；桌面默认展开，手机默认收起。
- 页脚：无。应用没有需要重复展示的站点地图。

## Theme — 月雾樱紫

主签名为深色画布，保留同色相的浅色伴随模式。紫色只负责纸面和层级，唯一强调色为樱粉。

| 令牌 | 深色 | 浅色 |
| --- | --- | --- |
| `--color-paper` | `oklch(15% 0.025 305)` | `oklch(96% 0.018 315)` |
| `--color-paper-2` | `oklch(19% 0.030 305)` | `oklch(93% 0.022 315)` |
| `--color-paper-3` | `oklch(23% 0.035 305)` | `oklch(89% 0.026 315)` |
| `--color-ink` | `oklch(94% 0.018 325)` | `oklch(21% 0.025 305)` |
| `--color-muted` | `oklch(68% 0.025 310)` | `oklch(42% 0.028 305)` |
| `--color-accent` | `oklch(75% 0.150 350)` | `oklch(64% 0.180 350)` |
| `--color-focus` | `oklch(84% 0.180 350)` | `oklch(50% 0.210 350)` |

主题轴：`dark（附 light companion） / geometric-sans / chromatic-dusty-pink`。

## Typography

- Display：`Avenir Next Condensed` / `Avenir Next`，700，正常体。
- Body：`Avenir Next` / `PingFang SC`，400，正常体。
- Sidebar art：`Snell Roundhand` / `Segoe Script` / `Brush Script MT`，用于左侧品牌和页标题旁的纯数字统计。
- Auth wordmark：`Bodoni 72` / `Didot`，600，正常体，只用于登录页品牌名称。
- 不加载在线字体或字体运行时；在局域网和断网环境中保持可用。
- 照片和相册数量不显示单位，使用斜体艺术数字；页面标题本身保持正常体、单行省略，不使用渐变文字。

## Geometry and spacing

- 4pt 间距体系，实际值以根目录 `tokens.css` 为准。
- 展开侧栏：`9.5rem`（152 px），紧贴 `Juen's` 艺术字与樱花按钮形成的品牌行；桌面折叠仍为 `4rem`（64 px）。
- 手机常驻侧栏：`3.5rem`（56 px）；展开时覆盖内容，不挤压瀑布流。
- 控件以 2–6 px 小圆角为主；不使用大胶囊和柔软气泡卡片。
- 主题切换和侧栏折叠按钮没有可见边框；侧栏折叠按钮使用樱花线稿图标，仍保留 44 px 点击区与焦点环。
- 瀑布流间距：`8px`，CSS 与虚拟布局计算必须保持一致。

## Motion

- 仅动画 `transform` 与 `opacity`。
- 保留三种反馈：按钮按压、照片内部轻微缩放、侧栏文字淡入淡出。
- 焦点环即时出现，绝不动画。
- `prefers-reduced-motion` 下取消空间移动，最长 150 ms。

## Interaction rules

- 触控目标不小于 44 × 44 CSS px。
- Hover 必须有键盘 Focus 等价状态。
- 激活导航同时使用轮廓和菱形标记，不只依赖颜色。
- 登录错误同时使用文字与边框状态。
- 成功状态保持安静，不显示庆祝动画或 Toast。

## Per-page allowances

- 主图库不添加装饰图片；用户照片就是内容与视觉重心。
- 登录页可以使用 `/api/login-bg` 返回的真实图库照片。
- 大图层可以使用深色遮罩，但信息面板必须保持实色和清晰层级。

## What pages must share

- 樱粉强调色及其克制用量。
- 字体角色、锐利小圆角、按钮尺寸和焦点环。
- 两套主题的色相关系与 4pt 间距体系。

## Export

浏览器实际使用的完整原生 CSS 令牌位于根目录 `tokens.css`。项目不使用 Tailwind、DTCG 或 shadcn，因此不保留无运行意义的兼容导出。
