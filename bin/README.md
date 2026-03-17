# Bundled binaries

把随包分发的可执行文件放在这里。

## ffmpeg

- Windows：放置 `bin/ffmpeg.exe`
- Linux/macOS：放置 `bin/ffmpeg`

后端会在未设置 `FFMPEG_PATH` 时，优先尝试从该目录加载 ffmpeg。

