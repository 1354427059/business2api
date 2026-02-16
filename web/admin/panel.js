(function () {
  const API_KEY_STORAGE = "business2api.admin.api_key";
  const CHAT_TEMPLATE_STORAGE = "business2api.admin.chat_template";

  const state = {
    apiKey: localStorage.getItem(API_KEY_STORAGE) || "",
    accounts: [],
    accountsPage: 1,
    accountsPageSize: 10,
    filePage: 1,
    filePageSize: 20,
    fileTotalPage: 1,
    deletePreviewFiles: [],
    models: [],
  };

  const els = {
    apiKeyInput: document.getElementById("apiKeyInput"),
    saveKeyBtn: document.getElementById("saveKeyBtn"),
    serviceDot: document.getElementById("serviceDot"),
    serviceText: document.getElementById("serviceText"),

    statReady: document.getElementById("statReady"),
    statPending: document.getElementById("statPending"),
    statPendingExternal: document.getElementById("statPendingExternal"),
    statInvalid: document.getElementById("statInvalid"),
    statAvailableToday: document.getElementById("statAvailableToday"),

    actionLog: document.getElementById("actionLog"),
    reloadPoolBtn: document.getElementById("reloadPoolBtn"),
    triggerRegisterBtn: document.getElementById("triggerRegisterBtn"),
    registerCountInput: document.getElementById("registerCountInput"),
    refreshAllBtn: document.getElementById("refreshAllBtn"),

    accountStateFilter: document.getElementById("accountStateFilter"),
    accountStatusFilter: document.getElementById("accountStatusFilter"),
    accountQFilter: document.getElementById("accountQFilter"),
    applyAccountFilterBtn: document.getElementById("applyAccountFilterBtn"),
    accountsBody: document.getElementById("accountsBody"),
    accountsPrevBtn: document.getElementById("accountsPrevBtn"),
    accountsNextBtn: document.getElementById("accountsNextBtn"),
    accountsPageInfo: document.getElementById("accountsPageInfo"),

    fileStateFilter: document.getElementById("fileStateFilter"),
    fileStatusFilter: document.getElementById("fileStatusFilter"),
    fileQFilter: document.getElementById("fileQFilter"),
    applyFileFilterBtn: document.getElementById("applyFileFilterBtn"),
    filesBody: document.getElementById("filesBody"),
    filesPrevBtn: document.getElementById("filesPrevBtn"),
    filesNextBtn: document.getElementById("filesNextBtn"),
    filesPageInfo: document.getElementById("filesPageInfo"),
    exportZipBtn: document.getElementById("exportZipBtn"),
    importFileInput: document.getElementById("importFileInput"),
    previewDeleteBtn: document.getElementById("previewDeleteBtn"),
    executeDeleteBtn: document.getElementById("executeDeleteBtn"),
    fileActionLog: document.getElementById("fileActionLog"),

    refreshModelsBtn: document.getElementById("refreshModelsBtn"),
    modelsOutput: document.getElementById("modelsOutput"),
    chatModelSelect: document.getElementById("chatModelSelect"),
    streamSwitch: document.getElementById("streamSwitch"),
    sendChatBtn: document.getElementById("sendChatBtn"),
    saveTemplateBtn: document.getElementById("saveTemplateBtn"),
    chatPromptInput: document.getElementById("chatPromptInput"),
    chatMeta: document.getElementById("chatMeta"),
    chatOutput: document.getElementById("chatOutput"),
  };

  function setServiceStatus(ok, text) {
    els.serviceDot.className = ok ? "dot dot-on" : "dot dot-off";
    els.serviceText.textContent = text;
  }

  function logTo(el, title, payload) {
    const now = new Date().toLocaleTimeString();
    const line = `[${now}] ${title}\n${typeof payload === "string" ? payload : JSON.stringify(payload, null, 2)}`;
    el.textContent = line;
  }

  function formatDate(value) {
    if (!value) return "-";
    const date = new Date(value);
    if (Number.isNaN(date.getTime())) return "-";
    return date.toLocaleString();
  }

  function formatBytes(value) {
    if (!Number.isFinite(value)) return "-";
    if (value < 1024) return `${value} B`;
    if (value < 1024 * 1024) return `${(value / 1024).toFixed(1)} KB`;
    return `${(value / (1024 * 1024)).toFixed(2)} MB`;
  }

  function statusTag(status) {
    const s = (status || "").toLowerCase();
    if (["ready", "pending", "cooldown", "pending_external"].includes(s)) {
      return `<span class="tag tag-active">${s}</span>`;
    }
    if (s === "unknown") {
      return `<span class="tag tag-warn">unknown</span>`;
    }
    return `<span class="tag tag-invalid">${s || "invalid"}</span>`;
  }

  async function apiFetch(path, options = {}) {
    const headers = Object.assign({}, options.headers || {});
    if (state.apiKey) {
      headers.Authorization = `Bearer ${state.apiKey}`;
    }
    if (!headers["Content-Type"] && !(options.body instanceof FormData) && options.body) {
      headers["Content-Type"] = "application/json";
    }
    const resp = await fetch(path, Object.assign({}, options, { headers }));
    const text = await resp.text();
    let body = null;
    try {
      body = text ? JSON.parse(text) : null;
    } catch (e) {
      body = text;
    }
    if (!resp.ok) {
      throw new Error(`[${resp.status}] ${typeof body === "string" ? body : JSON.stringify(body)}`);
    }
    return body;
  }

  async function loadStatus() {
    try {
      const status = await apiFetch("/admin/status");
      setServiceStatus(true, "已连接");
      els.statReady.textContent = status.ready ?? "-";
      els.statPending.textContent = status.pending ?? "-";
      els.statPendingExternal.textContent = status.pending_external ?? "-";
      els.statInvalid.textContent = status.invalid ?? "0";
      els.statAvailableToday.textContent = status.available_today ?? "-";
      return status;
    } catch (err) {
      setServiceStatus(false, "鉴权失败或服务不可达");
      logTo(els.actionLog, "状态加载失败", err.message);
      return null;
    }
  }

  async function loadAccounts() {
    const params = new URLSearchParams({
      state: els.accountStateFilter.value,
      status: els.accountStatusFilter.value,
      q: els.accountQFilter.value,
    });
    const data = await apiFetch(`/admin/accounts?${params.toString()}`);
    state.accounts = Array.isArray(data.items) ? data.items : [];
    state.accountsPage = 1;
    renderAccounts();
  }

  function renderAccounts() {
    const total = state.accounts.length;
    const totalPage = Math.max(1, Math.ceil(total / state.accountsPageSize));
    state.accountsPage = Math.min(Math.max(1, state.accountsPage), totalPage);

    const start = (state.accountsPage - 1) * state.accountsPageSize;
    const rows = state.accounts.slice(start, start + state.accountsPageSize);
    els.accountsBody.innerHTML = rows
      .map((item) => {
        const validTag = item.is_valid
          ? '<span class="tag tag-active">有效</span>'
          : '<span class="tag tag-invalid">失效</span>';
        return `<tr>
          <td title="${item.email || ""}">${item.email_masked || item.email || "-"}</td>
          <td>${statusTag(item.status)}</td>
          <td>${validTag}</td>
          <td>${item.invalid_reason || "-"}</td>
          <td>${item.fail_count ?? 0}</td>
          <td>${item.daily_remaining ?? "-"}</td>
          <td>${formatDate(item.last_used)}</td>
        </tr>`;
      })
      .join("");

    if (!rows.length) {
      els.accountsBody.innerHTML = '<tr><td colspan="7">暂无数据</td></tr>';
    }
    els.accountsPageInfo.textContent = `第 ${state.accountsPage} / ${totalPage} 页，共 ${total} 条`;
  }

  async function loadFiles() {
    const params = new URLSearchParams({
      state: els.fileStateFilter.value,
      status: els.fileStatusFilter.value,
      q: els.fileQFilter.value,
      page: String(state.filePage),
      page_size: String(state.filePageSize),
    });
    const data = await apiFetch(`/admin/pool-files?${params.toString()}`);
    const items = Array.isArray(data.items) ? data.items : [];

    els.filesBody.innerHTML = items
      .map((item) => {
        const parseTag = item.parse_ok
          ? '<span class="tag tag-active">OK</span>'
          : '<span class="tag tag-invalid">Bad</span>';
        return `<tr>
          <td>${item.file_name}</td>
          <td>${item.email_from_filename}</td>
          <td title="${item.parse_error || ""}">${parseTag}</td>
          <td>${statusTag(item.pool_status)}</td>
          <td>${item.exists_in_pool ? "是" : "否"}</td>
          <td>${item.has_config_id ? "有" : "无"}</td>
          <td>${item.has_csesidx ? "有" : "无"}</td>
          <td>${formatBytes(item.size_bytes)}</td>
          <td>${formatDate(item.modified_at)}</td>
        </tr>`;
      })
      .join("");

    if (!items.length) {
      els.filesBody.innerHTML = '<tr><td colspan="9">暂无数据</td></tr>';
    }

    state.fileTotalPage = Math.max(1, data.total_page || 1);
    els.filesPageInfo.textContent = `第 ${data.page || state.filePage} / ${state.fileTotalPage} 页，共 ${data.total || 0} 条`;
  }

  async function loadModels() {
    const data = await apiFetch("/v1/models");
    const models = Array.isArray(data.data) ? data.data.map((it) => it.id).filter(Boolean) : [];
    state.models = models;
    els.modelsOutput.textContent = JSON.stringify(data, null, 2);
    els.chatModelSelect.innerHTML = models.map((m) => `<option value="${m}">${m}</option>`).join("");

    const savedTemplate = localStorage.getItem(CHAT_TEMPLATE_STORAGE);
    if (savedTemplate) {
      try {
        const parsed = JSON.parse(savedTemplate);
        if (parsed.prompt) els.chatPromptInput.value = parsed.prompt;
        if (parsed.model && models.includes(parsed.model)) els.chatModelSelect.value = parsed.model;
        els.streamSwitch.checked = Boolean(parsed.stream);
      } catch (e) {
        // ignore broken storage
      }
    }
  }

  function saveTemplate() {
    const payload = {
      model: els.chatModelSelect.value,
      prompt: els.chatPromptInput.value,
      stream: els.streamSwitch.checked,
    };
    localStorage.setItem(CHAT_TEMPLATE_STORAGE, JSON.stringify(payload));
    logTo(els.chatOutput, "模板已保存", payload);
  }

  async function sendChatTest() {
    const model = els.chatModelSelect.value;
    const prompt = els.chatPromptInput.value.trim();
    const stream = els.streamSwitch.checked;
    if (!model) {
      logTo(els.chatOutput, "请求被阻止", "请先加载模型列表");
      return;
    }
    if (!prompt) {
      logTo(els.chatOutput, "请求被阻止", "提示词不能为空");
      return;
    }

    const payload = {
      model,
      stream,
      messages: [{ role: "user", content: prompt }],
    };

    const headers = {
      "Content-Type": "application/json",
    };
    if (state.apiKey) {
      headers.Authorization = `Bearer ${state.apiKey}`;
    }

    const started = performance.now();
    els.chatOutput.textContent = "";
    els.chatMeta.textContent = "请求中...";

    try {
      const resp = await fetch("/v1/chat/completions", {
        method: "POST",
        headers,
        body: JSON.stringify(payload),
      });

      if (!resp.ok) {
        const body = await resp.text();
        const elapsed = Math.round(performance.now() - started);
        els.chatMeta.textContent = `失败: HTTP ${resp.status} | ${elapsed}ms`;
        els.chatOutput.textContent = body;
        return;
      }

      if (!stream) {
        const json = await resp.json();
        const elapsed = Math.round(performance.now() - started);
        els.chatMeta.textContent = `完成: HTTP ${resp.status} | ${elapsed}ms`;
        els.chatOutput.textContent = JSON.stringify(json, null, 2);
        return;
      }

      const reader = resp.body.getReader();
      const decoder = new TextDecoder("utf-8");
      let buf = "";
      let streamText = "";

      while (true) {
        const { value, done } = await reader.read();
        if (done) break;
        buf += decoder.decode(value, { stream: true });

        const lines = buf.split("\n");
        buf = lines.pop() || "";

        for (const line of lines) {
          const trimmed = line.trim();
          if (!trimmed.startsWith("data:")) continue;
          const data = trimmed.slice(5).trim();
          if (!data) continue;
          if (data === "[DONE]") {
            const elapsed = Math.round(performance.now() - started);
            els.chatMeta.textContent = `流式完成: HTTP ${resp.status} | ${elapsed}ms`;
            return;
          }
          try {
            const json = JSON.parse(data);
            const delta = json?.choices?.[0]?.delta?.content || "";
            if (delta) {
              streamText += delta;
              els.chatOutput.textContent = streamText;
            }
          } catch (e) {
            streamText += `${data}\n`;
            els.chatOutput.textContent = streamText;
          }
        }
      }
      const elapsed = Math.round(performance.now() - started);
      els.chatMeta.textContent = `流式结束: HTTP ${resp.status} | ${elapsed}ms`;
    } catch (err) {
      const elapsed = Math.round(performance.now() - started);
      els.chatMeta.textContent = `异常: ${elapsed}ms`;
      els.chatOutput.textContent = err.message;
    }
  }

  async function exportPoolFiles() {
    const params = new URLSearchParams({
      state: els.fileStateFilter.value,
      status: els.fileStatusFilter.value,
      q: els.fileQFilter.value,
    });

    const headers = {};
    if (state.apiKey) {
      headers.Authorization = `Bearer ${state.apiKey}`;
    }

    const resp = await fetch(`/admin/pool-files/export?${params.toString()}`, { headers });
    if (!resp.ok) {
      const text = await resp.text();
      throw new Error(`[${resp.status}] ${text}`);
    }

    const blob = await resp.blob();
    const url = URL.createObjectURL(blob);
    const a = document.createElement("a");
    const disposition = resp.headers.get("Content-Disposition") || "";
    const match = disposition.match(/filename="?([^\"]+)"?/i);
    a.href = url;
    a.download = match ? match[1] : "pool-export.zip";
    a.click();
    URL.revokeObjectURL(url);
  }

  async function importPoolFiles(file) {
    const form = new FormData();
    form.append("file", file);
    form.append("overwrite", "true");

    const headers = {};
    if (state.apiKey) {
      headers.Authorization = `Bearer ${state.apiKey}`;
    }

    const resp = await fetch("/admin/pool-files/import", {
      method: "POST",
      headers,
      body: form,
    });

    const text = await resp.text();
    let body;
    try {
      body = text ? JSON.parse(text) : {};
    } catch (e) {
      body = text;
    }

    if (!resp.ok) {
      throw new Error(`[${resp.status}] ${typeof body === "string" ? body : JSON.stringify(body)}`);
    }
    return body;
  }

  async function previewDeleteInvalid() {
    const data = await apiFetch("/admin/pool-files/delete-invalid/preview", { method: "POST" });
    const candidates = Array.isArray(data.candidates) ? data.candidates : [];
    state.deletePreviewFiles = candidates.map((it) => it.file_name).filter(Boolean);
    logTo(els.fileActionLog, `预览完成: ${state.deletePreviewFiles.length} 个候选`, data);
  }

  async function executeDeleteInvalid() {
    if (!state.deletePreviewFiles.length) {
      throw new Error("请先执行预览，再确认删除");
    }
    const data = await apiFetch("/admin/pool-files/delete-invalid/execute", {
      method: "POST",
      body: JSON.stringify({ files: state.deletePreviewFiles, auto_backup: true }),
    });
    state.deletePreviewFiles = [];
    logTo(els.fileActionLog, "删除执行完成", data);
  }

  async function triggerRegister() {
    const count = Math.max(1, Math.min(20, Number(els.registerCountInput.value || 1)));
    const data = await apiFetch("/admin/registrar/trigger-register", {
      method: "POST",
      body: JSON.stringify({ count }),
    });
    logTo(els.actionLog, `已触发注册 count=${count}`, data);
  }

  async function reloadPool() {
    const data = await apiFetch("/admin/refresh", { method: "POST", body: "{}" });
    logTo(els.actionLog, "号池已刷新", data);
  }

  async function refreshAll() {
    await Promise.all([loadStatus(), loadAccounts(), loadFiles(), loadModels()]);
  }

  function bindEvents() {
    els.apiKeyInput.value = state.apiKey;
    els.saveKeyBtn.addEventListener("click", async () => {
      state.apiKey = els.apiKeyInput.value.trim();
      localStorage.setItem(API_KEY_STORAGE, state.apiKey);
      await refreshAll();
    });

    els.reloadPoolBtn.addEventListener("click", async () => {
      try {
        await reloadPool();
        await refreshAll();
      } catch (err) {
        logTo(els.actionLog, "刷新失败", err.message);
      }
    });

    els.triggerRegisterBtn.addEventListener("click", async () => {
      try {
        await triggerRegister();
      } catch (err) {
        logTo(els.actionLog, "触发注册失败", err.message);
      }
    });

    els.refreshAllBtn.addEventListener("click", async () => {
      try {
        await refreshAll();
      } catch (err) {
        logTo(els.actionLog, "刷新失败", err.message);
      }
    });

    els.applyAccountFilterBtn.addEventListener("click", async () => {
      try {
        await loadAccounts();
      } catch (err) {
        logTo(els.actionLog, "账号筛选失败", err.message);
      }
    });

    els.accountsPrevBtn.addEventListener("click", () => {
      state.accountsPage -= 1;
      renderAccounts();
    });
    els.accountsNextBtn.addEventListener("click", () => {
      state.accountsPage += 1;
      renderAccounts();
    });

    els.applyFileFilterBtn.addEventListener("click", async () => {
      state.filePage = 1;
      try {
        await loadFiles();
      } catch (err) {
        logTo(els.fileActionLog, "文件筛选失败", err.message);
      }
    });

    els.filesPrevBtn.addEventListener("click", async () => {
      if (state.filePage <= 1) return;
      state.filePage -= 1;
      try {
        await loadFiles();
      } catch (err) {
        logTo(els.fileActionLog, "翻页失败", err.message);
      }
    });

    els.filesNextBtn.addEventListener("click", async () => {
      if (state.filePage >= state.fileTotalPage) return;
      state.filePage += 1;
      try {
        await loadFiles();
      } catch (err) {
        logTo(els.fileActionLog, "翻页失败", err.message);
      }
    });

    els.exportZipBtn.addEventListener("click", async () => {
      try {
        await exportPoolFiles();
        logTo(els.fileActionLog, "导出成功", "ZIP 已开始下载");
      } catch (err) {
        logTo(els.fileActionLog, "导出失败", err.message);
      }
    });

    els.importFileInput.addEventListener("change", async (evt) => {
      const file = evt.target.files && evt.target.files[0];
      if (!file) return;
      try {
        const data = await importPoolFiles(file);
        logTo(els.fileActionLog, "导入完成", data);
        evt.target.value = "";
        await refreshAll();
      } catch (err) {
        logTo(els.fileActionLog, "导入失败", err.message);
      }
    });

    els.previewDeleteBtn.addEventListener("click", async () => {
      try {
        await previewDeleteInvalid();
      } catch (err) {
        logTo(els.fileActionLog, "预览失败", err.message);
      }
    });

    els.executeDeleteBtn.addEventListener("click", async () => {
      try {
        await executeDeleteInvalid();
        await refreshAll();
      } catch (err) {
        logTo(els.fileActionLog, "删除失败", err.message);
      }
    });

    els.refreshModelsBtn.addEventListener("click", async () => {
      try {
        await loadModels();
      } catch (err) {
        logTo(els.modelsOutput, "模型刷新失败", err.message);
      }
    });

    els.sendChatBtn.addEventListener("click", sendChatTest);
    els.saveTemplateBtn.addEventListener("click", saveTemplate);
  }

  async function bootstrap() {
    bindEvents();
    try {
      await refreshAll();
    } catch (err) {
      setServiceStatus(false, "初始化失败");
      logTo(els.actionLog, "初始化失败", err.message);
    }
  }

  bootstrap();
})();
