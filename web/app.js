const $ = (id) => document.getElementById(id);

const elFile = $("file");
const elStart = $("start");
const elStatus = $("status");
const elJob = $("job");
const elLog = $("log");
const elVideo = $("video");

let pollTimer = null;

function setStatus(text) {
  elStatus.textContent = text;
}

function log(line) {
  const ts = new Date().toLocaleTimeString();
  elLog.textContent = `[${ts}] ${line}\n` + elLog.textContent;
}

function setJob(id) {
  elJob.textContent = id || "-";
}

function canUseHlsJs() {
  return window.Hls && window.Hls.isSupported();
}

function playHls(url) {
  // Safari supports HLS natively
  if (elVideo.canPlayType("application/vnd.apple.mpegurl")) {
    elVideo.src = url;
    elVideo.play().catch(() => {});
    return;
  }

  if (!canUseHlsJs()) {
    log("当前浏览器不支持 hls.js（或不支持 MSE）。请换 Chrome/Edge。");
    return;
  }

  const hls = new window.Hls({
    lowLatencyMode: false,
    backBufferLength: 90,
  });
  hls.loadSource(url);
  hls.attachMedia(elVideo);

  hls.on(window.Hls.Events.ERROR, (_evt, data) => {
    if (data && data.fatal) {
      log(`播放错误: ${data.type} / ${data.details}`);
      hls.destroy();
    }
  });
}

async function uploadFile(file) {
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
  const json = await res.json();
  if (!json.jobId) throw new Error("upload_failed: missing jobId");
  return json.jobId;
}

async function startJob(jobId) {
  setStatus("启动转码…");
  const res = await fetch(`/api/jobs/${jobId}/start`, { method: "POST" });
  if (!res.ok) {
    const txt = await res.text();
    throw new Error(`start_failed: ${res.status} ${txt}`);
  }
  const json = await res.json();
  if (!json.playlistUrl) throw new Error("start_failed: missing playlistUrl");
  return json.playlistUrl;
}

async function fetchJob(jobId) {
  const res = await fetch(`/api/jobs/${jobId}`);
  if (!res.ok) return null;
  return await res.json();
}

function startPolling(jobId, onReadyUrl) {
  stopPolling();
  pollTimer = setInterval(async () => {
    const j = await fetchJob(jobId);
    if (!j) return;

    if (j.status) setStatus(j.status);
    if (j.error) log(`后端错误: ${j.error}`);

    if (j.status === "ready" && j.playlistUrl) {
      stopPolling();
      onReadyUrl(j.playlistUrl);
    }
  }, 1000);
}

function stopPolling() {
  if (pollTimer) clearInterval(pollTimer);
  pollTimer = null;
}

elFile.addEventListener("change", () => {
  const f = elFile.files && elFile.files[0];
  elStart.disabled = !f;
  if (f) log(`已选择文件: ${f.name}`);
});

elStart.addEventListener("click", async () => {
  const f = elFile.files && elFile.files[0];
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

    startPolling(jobId, (readyUrl) => {
      log("HLS 就绪，开始播放。");
      playHls(readyUrl);
    });
  } catch (e) {
    log(String(e && e.message ? e.message : e));
    setStatus("失败");
    elStart.disabled = false;
    return;
  }

  elStart.disabled = false;
});
