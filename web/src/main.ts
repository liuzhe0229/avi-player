import Hls from "hls.js";
import "./style.css";

const $ = <T extends HTMLElement>(id: string) => document.getElementById(id) as T;

const elFile = $<HTMLInputElement>("file");
const elStart = $<HTMLButtonElement>("start");
const elStatus = $<HTMLSpanElement>("status");
const elJob = $<HTMLSpanElement>("job");
const elLog = $<HTMLDivElement>("log");
const elVideo = $<HTMLVideoElement>("video");

let pollTimer: number | null = null;

function setStatus(text: string) {
  elStatus.textContent = text;
}

function log(line: string) {
  const ts = new Date().toLocaleTimeString();
  elLog.textContent = `[${ts}] ${line}\n` + elLog.textContent;
}

function setJob(id: string | null) {
  elJob.textContent = id ?? "-";
}

function playHls(url: string) {
  // Safari supports HLS natively
  if (elVideo.canPlayType("application/vnd.apple.mpegurl")) {
    elVideo.src = url;
    void elVideo.play().catch(() => { });
    return;
  }

  if (!Hls.isSupported()) {
    log("当前浏览器不支持 MSE/HLS.js。请换 Chrome/Edge。");
    return;
  }

  const hls = new Hls({
    lowLatencyMode: false,
    backBufferLength: 90,
    // When starting transcoding, the playlist may 404 briefly until ffmpeg writes it.
    manifestLoadingMaxRetry: 20,
    manifestLoadingRetryDelay: 500,
    manifestLoadingMaxRetryTimeout: 30_000,
  });
  hls.loadSource(url);
  hls.attachMedia(elVideo);

  hls.on(Hls.Events.ERROR, (_evt, data) => {
    if (data?.fatal) {
      log(`播放错误: ${data.type} / ${data.details}`);
      hls.destroy();
    }
  });
}

async function waitForUrl(url: string, timeoutMs: number): Promise<void> {
  const start = Date.now();
  while (Date.now() - start < timeoutMs) {
    try {
      const res = await fetch(url, { method: "GET" });
      if (res.ok) return;
    } catch {
      // ignore and retry
    }
    await new Promise((r) => window.setTimeout(r, 500));
  }
}

async function uploadFile(file: File): Promise<string> {
  setStatus("上传中…");
  log(`开始上传: ${file.name} (${(file.size / 1024 / 1024).toFixed(1)} MB)`);

  const fd = new FormData();
  fd.append("file", file, file.name);

  const res = await fetch("/api/upload", {
    method: "POST",
    body: fd,
  });
  if (!res.ok) {
    const txt = await res.text();
    throw new Error(`upload_failed: ${res.status} ${txt}`);
  }
  const json = (await res.json()) as { jobId?: string };
  if (!json.jobId) throw new Error("upload_failed: missing jobId");
  return json.jobId;
}

async function startJob(jobId: string): Promise<string> {
  setStatus("启动转码…");
  const res = await fetch(`/api/jobs/${jobId}/start`, { method: "POST" });
  if (!res.ok) {
    const txt = await res.text();
    throw new Error(`start_failed: ${res.status} ${txt}`);
  }
  const json = (await res.json()) as { playlistUrl?: string };
  if (!json.playlistUrl) throw new Error("start_failed: missing playlistUrl");
  return json.playlistUrl;
}

type JobResp = {
  id: string;
  status?: string;
  error?: string;
  playlistUrl?: string;
};

async function fetchJob(jobId: string): Promise<JobResp | null> {
  const res = await fetch(`/api/jobs/${jobId}`);
  if (!res.ok) return null;
  return (await res.json()) as JobResp;
}

function startPolling(jobId: string) {
  stopPolling();
  pollTimer = window.setInterval(async () => {
    const j = await fetchJob(jobId);
    if (!j) return;

    if (j.status) setStatus(j.status);
    if (j.error) log(`后端错误: ${j.error}`);
  }, 1000);
}

function stopPolling() {
  if (pollTimer !== null) window.clearInterval(pollTimer);
  pollTimer = null;
}

elFile.addEventListener("change", () => {
  const f = elFile.files?.[0];
  elStart.disabled = !f;
  if (f) log(`已选择文件: ${f.name}`);
});

elStart.addEventListener("click", async () => {
  const f = elFile.files?.[0];
  if (!f) return;

  elStart.disabled = true;
  elVideo.removeAttribute("src");
  elVideo.load();
  setJob(null);

  try {
    const jobId = await uploadFile(f);
    setJob(jobId);
    setStatus("uploaded");

    const playlistUrl = await startJob(jobId);
    log(`HLS 播放列表: ${playlistUrl}`);

    // 关键改动：不等待后端标记 ready，拿到 m3u8 URL 就直接尝试播放。
    // hls.js 会持续拉取，等 m3u8/分片生成后即可出画面。
    log("等待播放列表生成（无需等待转码完成）…");
    await waitForUrl(playlistUrl, 120_000);
    log("开始播放…");
    playHls(playlistUrl);
    startPolling(jobId);
  } catch (e) {
    log(String(e instanceof Error ? e.message : e));
    setStatus("失败");
  } finally {
    elStart.disabled = false;
  }
});

