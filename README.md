# 美人

家里 Unraid 上用的本地图片瀑布流相册。打开页面就能看，不用登录。点一张图看大图，再点大图回到刚才的位置。

## 启动

1. 复制 `.env.example` 为 `.env`
2. 改 `PHOTOS_DIR` 为你的照片目录（会递归扫描子文件夹）
3. 需要的话改 `HOST_PORT`（容器内固定是 5001，改的是电脑/Unraid 上的端口）
4. 在本目录执行：

```bash
docker compose up -d
```

浏览器打开 `http://服务器IP:HOST_PORT`。

## 环境变量

写在 `.env` 里即可。

| 变量 | 默认 | 含义 |
|---|---|---|
| `HOST_PORT` | `5001` | 宿主机端口。容器内永远是 5001 |
| `PHOTOS_DIR` | `./photos` | 宿主机上的照片目录 |
| `DATA_DIR` | `./data` | 索引和缩略图 |
| `TZ` | `Asia/Shanghai` | 日志时间 |
| `SCAN_EVERY` | `2m` | 自动再扫间隔，最短 10 秒 |

加了新照片可以等这一轮自动扫描，也可以：

```bash
docker compose restart
```

## 支持的图片

`.jpg` `.jpeg` `.png` `.webp` `.gif`（大小写都行）。子文件夹会进去。名字以 `.` 开头的文件或文件夹会跳过。不支持 HEIC / RAW。

## 看日志

```bash
docker compose logs -f
```

## 停止

```bash
docker compose down
```

数据和缩略图在 `DATA_DIR`，照片本身只读挂载，不会被改。
