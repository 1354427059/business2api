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
    activeTab: "console",
    session: {
      authenticated: false,
      username: "",
      expiresAt: "",
    },
    logStream: {
      eventSource: null,
      paused: false,
      autoScroll: true,
      source: "all",
      level: "all",
    },
  };

  const els = {
    apiKeyInput: document.getElementById("apiKeyInput"),
    saveKeyBtn: document.getElementById("saveKeyBtn"),
    serviceDot: document.getElementById("serviceDot"),
    serviceText: document.getElementById("serviceText"),
    sessionInfo: document.getElementById("sessionInfo"),
    openChangePwdBtn: document.getElementById("openChangePwdBtn"),
    logoutBtn: document.getElementById("logoutBtn"),

    loginSection: document.getElementById("loginSection"),
    appSection: document.getElementById("appSection"),
    loginUsername: document.getElementById("loginUsername"),
    loginPassword: document.getElementById("loginPassword"),
    loginBtn: document.getElementById("loginBtn"),
    loginMessage: document.getElementById("loginMessage"),

    tabBtnConsole: document.getElementById("tabBtnConsole"),
    tabBtnLogs: document.getElementById("tabBtnLogs"),
    tabConsole: document.getElementById("tabConsole"),
    tabLogs: document.getElementById("tabLogs"),

    statReady: document.getElementById("statReady"),
    statPending: document.getElementById("statPending"),
    statPendingExternal: document.getElementById("statPendingExternal"),
    statInvalid: document.getElementById("statInvalid"),
    statAvailableToday: document.getElementById("statAvailableToday"),
    statRegistrarSuccessRate: document.getElementById("statRegistrarSuccessRate"),
    statRegistrarFallbackRatio: document.getElementById("statRegistrarFallbackRatio"),
    statRegistrarSampleSize: document.getElementById("statRegistrarSampleSize"),
    statRegistrarThrottled: document.getElementById("statRegistrarThrottled"),

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

    logStreamStatus: document.getElementById("logStreamStatus"),
    logSourceSelect: document.getElementById("logSourceSelect"),
    logLevelSelect: document.getElementById("logLevelSelect"),
    toggleLogStreamBtn: document.getElementById("toggleLogStreamBtn"),
    clearLogViewBtn: document.getElementById("clearLogViewBtn"),
    autoScrollSwitch: document.getElementById("autoScrollSwitch"),
    logStreamOutput: document.getElementById("logStreamOutput"),

    passwordModal: document.getElementById("passwordModal"),
    newPasswordInput: document.getElementById("newPasswordInput"),
    submitChangePwdBtn: document.getElementById("submitChangePwdBtn"),
    closeChangePwdBtn: document.getElementById("closeChangePwdBtn"),
  };

  function escapeHtml(value) {
    return String(value || "")
      .replaceAll("&", "&amp;")
      .replaceAll("<", "&lt;")
      .replaceAll(">", "&gt;")
      .replaceAll('"', "&quot;")
      .replaceAll("'", "&#39;");
  }

  function setServiceStatus(ok, text) {
    els.serviceDot.className = ok ? "dot dot-on" : "dot dot-off";
    els.serviceText.textContent = text;
  }

  function setLogStreamStatus(text, cls) {
    els.logStreamStatus.textContent = text;
    els.logStreamStatus.className = `status-badge ${cls}`;
  }

  function appendLog(el, title, payload) {
    const now = new Date().toLocaleTimeString();
    const body = typeof payload === "string" ? payload : JSON.stringify(payload, null, 2);
    const line = `[${now}] ${title}\n${body}`;
    el.textContent = line + (el.textContent ? `\n\n${el.textContent}` : "");
  }

  function setLoginMessage(message, isError) {
    els.loginMessage.textContent = message;
    els.loginMessage.style.color = isError ? "#b42318" : "#5f6c7b";
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

  function formatRatio(value) {
    if (!Number.isFinite(value)) return "-";
    return `${(value * 100).toFixed(1)}%`;
  }

  function statusTag(status) {
    const s = (status || "").toLowerCase();
    if (["ready", "pending", "cooldown", "pending_external"].includes(s)) {
      return `<span class="tag tag-active">${escapeHtml(s)}</span>`;
    }
    if (s === "unknown") {
      return '<span class="tag tag-warn">unknown</span>';
    }
    return `<span class="tag tag-invalid">${escapeHtml(s || "invalid")}</span>`;
  }

  function parseResponsePayload(text) {
    if (!text) return null;
    try {
      return JSON.parse(text);
    } catch (e) {
      return text;
    }
  }

  function formatError(status, payload) {
    const body = typeof payload === "string" ? payload : JSON.stringify(payload || {});
    return `[${status}] ${body}`;
  }

  async function panelFetch(path, options = {}) {
    const headers = Object.assign({}, options.headers || {});
    if (state.apiKey) {
      headers.Authorization = `Bearer ${state.apiKey}`;
    }
    if (!headers["Content-Type"] && !(options.body instanceof FormData) && options.body) {
      headers["Content-Type"] = "application/json";
    }

    const resp = await fetch(path, Object.assign({}, options, {
      headers,
      credentials: "same-origin",
    }));
    const text = await resp.text();
    const body = parseResponsePayload(text);

    if (!resp.ok) {
      if (resp.status === 401 && !path.startsWith("/admin/panel/login") && !path.startsWith("/admin/panel/me")) {
        setAuthState(false);
        setLoginMessage("登录已失效，请重新登录。", true);
      }
      throw new Error(formatError(resp.status, body));
    }
    return body;
  }

  async function llmFetch(path, options = {}) {
    if (!state.apiKey) {
      throw new Error("请先填写 API Key（用于 /v1 模型与调用测试）");
    }

    const headers = Object.assign({}, options.headers || {});
    headers.Authorization = `Bearer ${state.apiKey}`;
    if (!headers["Content-Type"] && !(options.body instanceof FormData) && options.body) {
      headers["Content-Type"] = "application/json";
    }

    const resp = await fetch(path, Object.assign({}, options, {
      headers,
      credentials: "same-origin",
    }));
    const text = await resp.text();
    const body = parseResponsePayload(text);

    if (!resp.ok) {
      throw new Error(formatError(resp.status, body));
    }
    return body;
  }

  function setAuthState(authenticated, info = {}) {
    state.session.authenticated = Boolean(authenticated);
    state.session.username = authenticated ? info.username || "admin" : "";
    state.session.expiresAt = authenticated ? info.expires_at || "" : "";

    els.loginSection.classList.toggle("hidden", authenticated);
    els.appSection.classList.toggle("hidden", !authenticated);
    els.openChangePwdBtn.disabled = !authenticated;
    els.logoutBtn.disabled = !authenticated;

    if (authenticated) {
      const expiresText = state.session.expiresAt ? `，过期：${formatDate(state.session.expiresAt)}` : "";
      els.sessionInfo.textContent = `已登录：${state.session.username}${expiresText}`;
      setLoginMessage("已登录，可直接使用管理能力。", false);
    } else {
      els.sessionInfo.textContent = "未登录";
      setServiceStatus(false, "未连接");
      closeLogStream();
      setLogStreamStatus("未连接", "status-muted");
      switchTab("console");
    }
  }

  async function loadSession() {
    try {
      const me = await panelFetch("/admin/panel/me");
      if (me && me.authenticated) {
        setAuthState(true, me);
        return true;
      }
      setAuthState(false);
      return false;
    } catch (err) {
      setAuthState(false);
      setLoginMessage(`会话检查失败：${err.message}`, true);
      return false;
    }
  }

  async function login() {
    const username = (els.loginUsername.value || "").trim();
    const password = (els.loginPassword.value || "").trim();
    if (!username || !password) {
      setLoginMessage("账号和密码不能为空。", true);
      return;
    }

    const data = await panelFetch("/admin/panel/login", {
      method: "POST",
      body: JSON.stringify({ username, password }),
    });

    els.loginPassword.value = "";
    setAuthState(true, data || {});
    setServiceStatus(true, "已连接");
    appendLog(els.actionLog, "登录成功", { username: data.username, expires_at: data.expires_at });
    await refreshAll();
  }

  async function logout() {
    try {
      await panelFetch("/admin/panel/logout", { method: "POST" });
    } catch (err) {
      appendLog(els.actionLog, "退出登录异常", err.message);
    }
    setAuthState(false);
    setLoginMessage("已退出登录。", false);
  }

  function openPasswordModal() {
    els.passwordModal.classList.remove("hidden");
    els.newPasswordInput.value = "";
    els.newPasswordInput.focus();
  }

  function closePasswordModal() {
    els.passwordModal.classList.add("hidden");
  }

  async function changePassword() {
    const newPassword = (els.newPasswordInput.value || "").trim();
    if (newPassword.length < 6) {
      appendLog(els.actionLog, "改密失败", "新密码长度至少 6 位");
      return;
    }

    const data = await panelFetch("/admin/panel/change-password", {
      method: "POST",
      body: JSON.stringify({ new_password: newPassword }),
    });

    closePasswordModal();
    appendLog(els.actionLog, "密码已修改", data);
    setAuthState(false);
    setLoginMessage("密码已更新，请使用新密码重新登录。", false);
  }

  async function loadStatus() {
    const status = await panelFetch("/admin/status");
    setServiceStatus(true, "已连接");
    els.statReady.textContent = status.ready ?? "-";
    els.statPending.textContent = status.pending ?? "-";
    els.statPendingExternal.textContent = status.pending_external ?? "-";
    els.statInvalid.textContent = status.invalid ?? "0";
    els.statAvailableToday.textContent = status.available_today ?? "-";
    return status;
  }

  async function loadRegistrarMetrics() {
    const metrics = await panelFetch("/admin/registrar/metrics");
    const successRate = Number(metrics.refresh_success_rate);
    const fallbackRatio = Number(metrics.fallback_ratio);
    const sampleSize = Number(metrics.sample_size);
    const throttled = Boolean(metrics.throttled);

    els.statRegistrarSuccessRate.textContent = formatRatio(successRate);
    els.statRegistrarFallbackRatio.textContent = formatRatio(fallbackRatio);
    els.statRegistrarSampleSize.textContent = Number.isFinite(sampleSize) ? String(sampleSize) : "-";
    els.statRegistrarThrottled.textContent = throttled ? "ON" : "OFF";
    els.statRegistrarThrottled.style.color = throttled ? "#c52f2f" : "#1e8f45";
    return metrics;
  }

  async function loadAccounts() {
    const params = new URLSearchParams({
      state: els.accountStateFilter.value,
      status: els.accountStatusFilter.value,
      q: els.accountQFilter.value,
    });
    const data = await panelFetch(`/admin/accounts?${params.toString()}`);
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
          <td title="${escapeHtml(item.email || "")}">${escapeHtml(item.email_masked || item.email || "-")}</td>
          <td>${statusTag(item.status)}</td>
          <td>${validTag}</td>
          <td>${escapeHtml(item.invalid_reason || "-")}</td>
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
    const data = await panelFetch(`/admin/pool-files?${params.toString()}`);
    const items = Array.isArray(data.items) ? data.items : [];

    els.filesBody.innerHTML = items
      .map((item) => {
        const parseTag = item.parse_ok
          ? '<span class="tag tag-active">OK</span>'
          : '<span class="tag tag-invalid">Bad</span>';
        return `<tr>
          <td>${escapeHtml(item.file_name || "-")}</td>
          <td>${escapeHtml(item.email_from_filename || "-")}</td>
          <td title="${escapeHtml(item.parse_error || "")}">${parseTag}</td>
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
    if (!state.apiKey) {
      els.modelsOutput.textContent = "未提供 API Key，无法访问 /v1/models。";
      els.chatModelSelect.innerHTML = '<option value="">请先填写 API Key</option>';
      return;
    }

    const data = await llmFetch("/v1/models");
    const models = Array.isArray(data.data) ? data.data.map((item) => item.id).filter(Boolean) : [];
    state.models = models;
    els.modelsOutput.textContent = JSON.stringify(data, null, 2);

    if (!models.length) {
      els.chatModelSelect.innerHTML = '<option value="">无可用模型</option>';
      return;
    }

    els.chatModelSelect.innerHTML = models.map((model) => `<option value="${escapeHtml(model)}">${escapeHtml(model)}</option>`).join("");

    const savedTemplate = localStorage.getItem(CHAT_TEMPLATE_STORAGE);
    if (savedTemplate) {
      try {
        const parsed = JSON.parse(savedTemplate);
        if (parsed.prompt) els.chatPromptInput.value = parsed.prompt;
        if (parsed.model && models.includes(parsed.model)) els.chatModelSelect.value = parsed.model;
        els.streamSwitch.checked = Boolean(parsed.stream);
      } catch (e) {
        // ignore
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
    appendLog(els.chatOutput, "模板已保存", payload);
  }

  async function sendChatTest() {
    const model = els.chatModelSelect.value;
    const prompt = els.chatPromptInput.value.trim();
    const stream = els.streamSwitch.checked;
    if (!state.apiKey) {
      appendLog(els.chatOutput, "请求被阻止", "调用测试依赖 API Key，请先在顶部保存 API Key。");
      return;
    }
    if (!model) {
      appendLog(els.chatOutput, "请求被阻止", "请先加载模型列表");
      return;
    }
    if (!prompt) {
      appendLog(els.chatOutput, "请求被阻止", "提示词不能为空");
      return;
    }

    const payload = {
      model,
      stream,
      messages: [{ role: "user", content: prompt }],
    };

    const started = performance.now();
    els.chatOutput.textContent = "";
    els.chatMeta.textContent = "请求中...";

    try {
      const resp = await fetch("/v1/chat/completions", {
        method: "POST",
        credentials: "same-origin",
        headers: {
          "Content-Type": "application/json",
          Authorization: `Bearer ${state.apiKey}`,
        },
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

    const resp = await fetch(`/admin/pool-files/export?${params.toString()}`, {
      headers,
      credentials: "same-origin",
    });
    if (!resp.ok) {
      const text = await resp.text();
      throw new Error(formatError(resp.status, parseResponsePayload(text)));
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
      credentials: "same-origin",
      body: form,
    });

    const text = await resp.text();
    const body = parseResponsePayload(text);
    if (!resp.ok) {
      throw new Error(formatError(resp.status, body));
    }
    return body;
  }

  async function previewDeleteInvalid() {
    const data = await panelFetch("/admin/pool-files/delete-invalid/preview", { method: "POST" });
    const candidates = Array.isArray(data.candidates) ? data.candidates : [];
    state.deletePreviewFiles = candidates.map((item) => item.file_name).filter(Boolean);
    appendLog(els.fileActionLog, `预览完成: ${state.deletePreviewFiles.length} 个候选`, data);
  }

  async function executeDeleteInvalid() {
    if (!state.deletePreviewFiles.length) {
      throw new Error("请先执行预览，再确认删除");
    }
    const data = await panelFetch("/admin/pool-files/delete-invalid/execute", {
      method: "POST",
      body: JSON.stringify({ files: state.deletePreviewFiles, auto_backup: true }),
    });
    state.deletePreviewFiles = [];
    appendLog(els.fileActionLog, "删除执行完成", data);
  }

  async function triggerRegister() {
    const count = Math.max(1, Math.min(20, Number(els.registerCountInput.value || 1)));
    const data = await panelFetch("/admin/registrar/trigger-register", {
      method: "POST",
      body: JSON.stringify({ count }),
    });
    appendLog(els.actionLog, `已触发注册 count=${count}`, data);

    if (state.logStream.paused) {
      state.logStream.paused = false;
      els.toggleLogStreamBtn.textContent = "暂停日志流";
    }
    switchTab("logs");
    restartLogStream();
    if (state.logStream.autoScroll) {
      els.logStreamOutput.scrollTop = els.logStreamOutput.scrollHeight;
    }
  }

  async function reloadPool() {
    const data = await panelFetch("/admin/refresh", { method: "POST", body: "{}" });
    appendLog(els.actionLog, "号池已刷新", data);
  }

  async function refreshAll() {
    const results = await Promise.allSettled([loadStatus(), loadRegistrarMetrics(), loadAccounts(), loadFiles(), loadModels()]);
    const failed = results.filter((item) => item.status === "rejected");
    if (failed.length) {
      const reason = failed.map((item) => item.reason?.message || String(item.reason)).join("\n");
      appendLog(els.actionLog, "部分加载失败", reason);
    }
  }

  function trimLogOutput() {
    const lines = els.logStreamOutput.textContent.split("\n");
    if (lines.length <= 3000) {
      return;
    }
    els.logStreamOutput.textContent = lines.slice(lines.length - 3000).join("\n");
  }

  function appendLogLines(lines) {
    if (!Array.isArray(lines) || lines.length === 0) {
      return;
    }
    const chunk = lines.join("\n");
    els.logStreamOutput.textContent += els.logStreamOutput.textContent ? `\n${chunk}` : chunk;
    trimLogOutput();
    if (state.logStream.autoScroll) {
      els.logStreamOutput.scrollTop = els.logStreamOutput.scrollHeight;
    }
  }

  function formatStreamLine(item) {
    const ts = formatDate(item.ts);
    const source = (item.source || "business2api").toString();
    const level = (item.level || "info").toString().toUpperCase();
    return `[${ts}] [${source}] [${level}] ${item.message || ""}`;
  }

  function closeLogStream() {
    if (state.logStream.eventSource) {
      state.logStream.eventSource.close();
      state.logStream.eventSource = null;
    }
  }

  function startLogStream(forceRestart) {
    if (!state.session.authenticated || state.logStream.paused) {
      return;
    }
    if (state.logStream.eventSource && !forceRestart) {
      return;
    }

    closeLogStream();
    const params = new URLSearchParams({
      source: state.logStream.source,
      level: state.logStream.level,
      bootstrap_limit: "200",
      poll_ms: "1000",
    });

    const es = new EventSource(`/admin/logs/stream?${params.toString()}`, { withCredentials: true });
    state.logStream.eventSource = es;
    setLogStreamStatus("连接中...", "status-muted");

    es.onopen = function () {
      setLogStreamStatus("实时流已连接", "status-ok");
    };

    es.addEventListener("logs", function (event) {
      try {
        const payload = JSON.parse(event.data || "{}");
        const items = Array.isArray(payload.items) ? payload.items : [];
        appendLogLines(items.map(formatStreamLine));
      } catch (err) {
        appendLogLines([`[${new Date().toLocaleString()}] [system] [WARN] 日志解析失败: ${err.message}`]);
      }
    });

    es.addEventListener("system", function (event) {
      let message = "系统通知";
      try {
        const payload = JSON.parse(event.data || "{}");
        if (payload.message) {
          message = payload.message;
        }
      } catch (err) {
        if (event.data) {
          message = event.data;
        }
      }
      appendLogLines([`[${new Date().toLocaleString()}] [system] [INFO] ${message}`]);
    });

    es.addEventListener("ping", function () {
      // 保活事件无需渲染
    });

    es.onerror = function () {
      if (state.logStream.paused) {
        return;
      }
      setLogStreamStatus("连接异常，自动重连中", "status-warn");
    };
  }

  function restartLogStream() {
    if (state.activeTab !== "logs") {
      return;
    }
    startLogStream(true);
  }

  function switchTab(tab) {
    state.activeTab = tab;
    const isConsole = tab === "console";
    els.tabBtnConsole.classList.toggle("active", isConsole);
    els.tabBtnLogs.classList.toggle("active", !isConsole);
    els.tabConsole.classList.toggle("active", isConsole);
    els.tabLogs.classList.toggle("active", !isConsole);

    if (isConsole) {
      closeLogStream();
      setLogStreamStatus(state.logStream.paused ? "已暂停" : "未连接", "status-muted");
      return;
    }

    if (!state.logStream.paused) {
      startLogStream(true);
    } else {
      setLogStreamStatus("已暂停", "status-muted");
    }
  }

  function bindEvents() {
    els.apiKeyInput.value = state.apiKey;

    els.saveKeyBtn.addEventListener("click", async () => {
      state.apiKey = (els.apiKeyInput.value || "").trim();
      localStorage.setItem(API_KEY_STORAGE, state.apiKey);
      appendLog(els.actionLog, "API Key 已保存", { configured: Boolean(state.apiKey) });
      if (state.session.authenticated) {
        await loadModels().catch((err) => appendLog(els.modelsOutput, "模型加载失败", err.message));
      }
    });

    els.loginBtn.addEventListener("click", async () => {
      try {
        await login();
      } catch (err) {
        setLoginMessage(`登录失败：${err.message}`, true);
      }
    });

    els.loginPassword.addEventListener("keydown", async (event) => {
      if (event.key !== "Enter") return;
      try {
        await login();
      } catch (err) {
        setLoginMessage(`登录失败：${err.message}`, true);
      }
    });

    els.logoutBtn.addEventListener("click", logout);
    els.openChangePwdBtn.addEventListener("click", openPasswordModal);
    els.closeChangePwdBtn.addEventListener("click", closePasswordModal);
    els.submitChangePwdBtn.addEventListener("click", async () => {
      try {
        await changePassword();
      } catch (err) {
        appendLog(els.actionLog, "改密失败", err.message);
      }
    });

    els.passwordModal.addEventListener("click", (event) => {
      if (event.target === els.passwordModal) {
        closePasswordModal();
      }
    });

    els.tabBtnConsole.addEventListener("click", () => switchTab("console"));
    els.tabBtnLogs.addEventListener("click", () => switchTab("logs"));

    els.reloadPoolBtn.addEventListener("click", async () => {
      try {
        await reloadPool();
        await refreshAll();
      } catch (err) {
        appendLog(els.actionLog, "刷新失败", err.message);
      }
    });

    els.triggerRegisterBtn.addEventListener("click", async () => {
      try {
        await triggerRegister();
      } catch (err) {
        appendLog(els.actionLog, "触发注册失败", err.message);
      }
    });

    els.refreshAllBtn.addEventListener("click", async () => {
      try {
        await refreshAll();
      } catch (err) {
        appendLog(els.actionLog, "刷新失败", err.message);
      }
    });

    els.applyAccountFilterBtn.addEventListener("click", async () => {
      try {
        await loadAccounts();
      } catch (err) {
        appendLog(els.actionLog, "账号筛选失败", err.message);
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
        appendLog(els.fileActionLog, "文件筛选失败", err.message);
      }
    });

    els.filesPrevBtn.addEventListener("click", async () => {
      if (state.filePage <= 1) return;
      state.filePage -= 1;
      try {
        await loadFiles();
      } catch (err) {
        appendLog(els.fileActionLog, "翻页失败", err.message);
      }
    });

    els.filesNextBtn.addEventListener("click", async () => {
      if (state.filePage >= state.fileTotalPage) return;
      state.filePage += 1;
      try {
        await loadFiles();
      } catch (err) {
        appendLog(els.fileActionLog, "翻页失败", err.message);
      }
    });

    els.exportZipBtn.addEventListener("click", async () => {
      try {
        await exportPoolFiles();
        appendLog(els.fileActionLog, "导出成功", "ZIP 已开始下载");
      } catch (err) {
        appendLog(els.fileActionLog, "导出失败", err.message);
      }
    });

    els.importFileInput.addEventListener("change", async (evt) => {
      const file = evt.target.files && evt.target.files[0];
      if (!file) return;
      try {
        const data = await importPoolFiles(file);
        appendLog(els.fileActionLog, "导入完成", data);
        evt.target.value = "";
        await refreshAll();
      } catch (err) {
        appendLog(els.fileActionLog, "导入失败", err.message);
      }
    });

    els.previewDeleteBtn.addEventListener("click", async () => {
      try {
        await previewDeleteInvalid();
      } catch (err) {
        appendLog(els.fileActionLog, "预览失败", err.message);
      }
    });

    els.executeDeleteBtn.addEventListener("click", async () => {
      try {
        await executeDeleteInvalid();
        await refreshAll();
      } catch (err) {
        appendLog(els.fileActionLog, "删除失败", err.message);
      }
    });

    els.refreshModelsBtn.addEventListener("click", async () => {
      try {
        await loadModels();
      } catch (err) {
        appendLog(els.modelsOutput, "模型刷新失败", err.message);
      }
    });

    els.sendChatBtn.addEventListener("click", sendChatTest);
    els.saveTemplateBtn.addEventListener("click", saveTemplate);

    els.logSourceSelect.addEventListener("change", () => {
      state.logStream.source = els.logSourceSelect.value;
      restartLogStream();
    });

    els.logLevelSelect.addEventListener("change", () => {
      state.logStream.level = els.logLevelSelect.value;
      restartLogStream();
    });

    els.toggleLogStreamBtn.addEventListener("click", () => {
      state.logStream.paused = !state.logStream.paused;
      els.toggleLogStreamBtn.textContent = state.logStream.paused ? "继续日志流" : "暂停日志流";
      if (state.logStream.paused) {
        closeLogStream();
        setLogStreamStatus("已暂停", "status-muted");
      } else {
        restartLogStream();
      }
    });

    els.clearLogViewBtn.addEventListener("click", () => {
      els.logStreamOutput.textContent = "";
    });

    els.autoScrollSwitch.addEventListener("change", () => {
      state.logStream.autoScroll = Boolean(els.autoScrollSwitch.checked);
    });
  }

  async function bootstrap() {
    bindEvents();

    state.logStream.source = els.logSourceSelect.value;
    state.logStream.level = els.logLevelSelect.value;
    state.logStream.autoScroll = Boolean(els.autoScrollSwitch.checked);

    const ok = await loadSession();
    if (!ok) {
      setLoginMessage("请输入账号和密码登录。", false);
      return;
    }

    try {
      await refreshAll();
    } catch (err) {
      appendLog(els.actionLog, "初始化失败", err.message);
    }
  }

  bootstrap();
})();
