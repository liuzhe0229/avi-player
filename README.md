# AVI Player Helper (Go + H5 + ffmpeg)

用 **Go 本地服务** 协助浏览器页面播放本地 **AVI 超大视频（几个 GB）**：浏览器上传 AVI → 本地服务调用 `ffmpeg` 转为 **HLS** → 前端用 `hls.js` 播放 `m3u8`。

## 依赖

- **Go**：建议 1.22+（Windows 安装后确保 `go` 在 PATH 里）
- **ffmpeg**：需要可执行文件
  - 方案 A（推荐随包分发）：把 `ffmpeg.exe` 放到项目的 `bin/ffmpeg.exe`（或打包产物同目录的 `bin/` 下）
  - 方案 B：系统已安装并加入 PATH（命令行能直接运行 `ffmpeg -version`）
  - 方案 C：设置环境变量 `FFMPEG_PATH` 指向 `ffmpeg.exe`

## 运行

在项目根目录（`d:/work/avi-player`）：

```bash
go run ./cmd/server
```

默认地址是 `http://127.0.0.1:8080`，浏览器打开即可看到页面。

### 随包分发 ffmpeg（无需用户安装）

把 `ffmpeg.exe` 放到：

- `d:/work/avi-player/bin/ffmpeg.exe`

服务启动时若未设置 `FFMPEG_PATH`，会自动优先尝试加载该路径。

## Web（Vite + TS + ESLint）

前端在 `web/` 目录，使用 Vite 启动本地开发服务，并把 `/api`、`/hls` 代理到 Go 后端。

```bash
cd web
npm install
npm run dev
```

然后打开 Vite 地址（默认 `http://127.0.0.1:5173`）。

### 可选环境变量

- `ADDR`：监听地址（默认 `127.0.0.1:8080`）
- `DATA_DIR`：数据目录（默认 `./data`，会生成 `uploads/` 与 `hls/`）
- `FFMPEG_PATH`：ffmpeg 可执行文件（默认 `ffmpeg`）
- `MAX_JOBS`：最多保留的 job 数（默认 `3`）
- `JOB_TTL_MINUTES`：job 过期时间（默认 `120` 分钟）

PowerShell 示例（ffmpeg 不在 PATH 时）：

```powershell
$env:FFMPEG_PATH="D:\tools\ffmpeg\bin\ffmpeg.exe"
go run .\cmd\server
```

## 使用方式（前端页面）

1. 打开 `http://127.0.0.1:8080`
2. 点击“选择 AVI 文件”
3. 点击“开始”
4. 看到状态变为 `ready` 后会自动开始播放（HLS 边转边播）

## API（最小可用）

- `POST /api/upload`：multipart 表单字段 `file`，返回 `{ "jobId": "..." }`
- `POST /api/jobs/{jobId}/start`：启动转码，返回 `{ "playlistUrl": "/hls/{jobId}/index.m3u8" }`
- `GET /api/jobs/{jobId}`：查看状态（`uploading/uploaded/transcoding/ready/finished/failed`）
- `GET /hls/{jobId}/index.m3u8`：HLS 播放列表
- `GET /api/health`：健康检查

## 常见问题

### 1) 浏览器为什么不能直接播 AVI？

大多数浏览器原生不支持 AVI 容器/编解码组合，所以这里通过 `ffmpeg` 转成浏览器更友好的 HLS（H.264 + AAC）。

### 2) 上传几个 GB 会不会很慢？

会受磁盘与本机回环网速影响；这套最小实现是“上传完成后开始转码”。如果你后续需要“上传同时就开始转码”，可以继续扩展为边写文件边转码（需要更复杂的管道/容错）。

### 3) 播放失败怎么看原因？

每个 job 的转码日志在：

- `data/hls/{jobId}/ffmpeg.log`

常见原因是 AVI 内部编码不被 ffmpeg 解码器支持，或音频编码缺失。

### 4) 临时文件会不会占满磁盘？

服务会定期清理：

- 默认只保留最近 `MAX_JOBS=3` 个 job，且超过 `JOB_TTL_MINUTES=120` 分钟会被删除

