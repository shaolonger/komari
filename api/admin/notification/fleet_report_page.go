package notification

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

func FleetReportSettingsPage(c *gin.Context) {
	c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(fleetReportSettingsHTML))
}

const fleetReportSettingsHTML = `<!doctype html>
<html lang="zh-CN">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>全局运维报告设置 - Komari</title>
  <style>
    :root {
      color-scheme: light;
      --bg: #f5f5f7;
      --panel: rgba(255, 255, 255, 0.86);
      --panel-strong: #fff;
      --text: #1d1d1f;
      --muted: #6e6e73;
      --line: rgba(0, 0, 0, 0.1);
      --blue: #007aff;
      --green: #34c759;
      --orange: #ff9500;
      --red: #ff3b30;
      --shadow: 0 18px 40px rgba(0, 0, 0, 0.08);
      font-family: -apple-system, BlinkMacSystemFont, "SF Pro Display", "SF Pro Text", "Segoe UI", sans-serif;
    }
    * { box-sizing: border-box; }
    body {
      margin: 0;
      min-height: 100vh;
      background: radial-gradient(circle at 18% 0%, rgba(0, 122, 255, 0.14), transparent 34%), var(--bg);
      color: var(--text);
    }
    .shell {
      width: min(1120px, calc(100vw - 32px));
      margin: 0 auto;
      padding: 28px 0 48px;
    }
    .nav {
      display: flex;
      align-items: center;
      justify-content: space-between;
      gap: 16px;
      margin-bottom: 22px;
    }
    .back {
      color: var(--blue);
      text-decoration: none;
      font-size: 14px;
      font-weight: 600;
    }
    h1 {
      margin: 0;
      font-size: clamp(28px, 5vw, 46px);
      line-height: 1.04;
      letter-spacing: 0;
    }
    .subtitle {
      margin: 10px 0 0;
      max-width: 760px;
      color: var(--muted);
      font-size: 15px;
      line-height: 1.65;
    }
    .grid {
      display: grid;
      grid-template-columns: 1.1fr 0.9fr;
      gap: 18px;
      margin-top: 24px;
      align-items: start;
    }
    .card {
      background: var(--panel);
      border: 1px solid var(--line);
      border-radius: 18px;
      box-shadow: var(--shadow);
      backdrop-filter: blur(22px);
      -webkit-backdrop-filter: blur(22px);
    }
    .card-inner { padding: 22px; }
    .card h2 {
      margin: 0 0 16px;
      font-size: 18px;
      line-height: 1.2;
      letter-spacing: 0;
    }
    .status-grid {
      display: grid;
      grid-template-columns: repeat(3, 1fr);
      gap: 10px;
      margin-bottom: 16px;
    }
    .status {
      min-height: 86px;
      padding: 14px;
      border-radius: 14px;
      background: rgba(255, 255, 255, 0.72);
      border: 1px solid var(--line);
    }
    .status span {
      display: block;
      color: var(--muted);
      font-size: 12px;
      margin-bottom: 10px;
    }
    .status strong {
      display: block;
      font-size: 22px;
      line-height: 1.05;
    }
    .status.good strong { color: var(--green); }
    .status.warn strong { color: var(--orange); }
    .status.bad strong { color: var(--red); }
    label {
      display: block;
      color: var(--muted);
      font-size: 13px;
      font-weight: 600;
      margin-bottom: 7px;
    }
    input[type="text"], input[type="number"], select {
      width: 100%;
      height: 44px;
      border: 1px solid var(--line);
      border-radius: 12px;
      padding: 0 12px;
      background: rgba(255, 255, 255, 0.78);
      color: var(--text);
      font: inherit;
      outline: none;
    }
    input:focus, select:focus {
      border-color: rgba(0, 122, 255, 0.62);
      box-shadow: 0 0 0 4px rgba(0, 122, 255, 0.12);
    }
    .row {
      display: grid;
      grid-template-columns: repeat(2, minmax(0, 1fr));
      gap: 14px;
      margin-top: 14px;
    }
    .switch-row, .check-row {
      display: flex;
      align-items: center;
      justify-content: space-between;
      gap: 16px;
      min-height: 56px;
      padding: 12px 14px;
      border: 1px solid var(--line);
      border-radius: 14px;
      background: rgba(255, 255, 255, 0.66);
    }
    .switch-row + .check-row, .check-row + .check-row { margin-top: 10px; }
    .switch-row p, .check-row p { margin: 4px 0 0; color: var(--muted); font-size: 12px; }
    .switch-row strong, .check-row strong { font-size: 14px; }
    input[type="checkbox"] {
      width: 22px;
      height: 22px;
      accent-color: var(--blue);
      flex: none;
    }
    .actions {
      display: flex;
      flex-wrap: wrap;
      align-items: center;
      gap: 10px;
      margin-top: 20px;
    }
    button {
      min-height: 42px;
      border: 1px solid var(--line);
      border-radius: 999px;
      padding: 0 18px;
      background: rgba(255, 255, 255, 0.78);
      color: var(--text);
      font: inherit;
      font-weight: 700;
      cursor: pointer;
    }
    button.primary {
      background: var(--blue);
      border-color: var(--blue);
      color: #fff;
    }
    button:disabled { opacity: 0.55; cursor: not-allowed; }
    .hint {
      margin: 10px 0 0;
      color: var(--muted);
      font-size: 12px;
      line-height: 1.55;
    }
    .last-list {
      display: grid;
      gap: 10px;
    }
    .last-item {
      padding: 13px 14px;
      border-radius: 14px;
      border: 1px solid var(--line);
      background: rgba(255, 255, 255, 0.68);
    }
    .last-item span {
      display: block;
      color: var(--muted);
      font-size: 12px;
      margin-bottom: 6px;
    }
    .last-item strong { font-size: 14px; }
    .notice {
      margin-top: 14px;
      padding: 13px 14px;
      border-radius: 14px;
      border: 1px solid rgba(255, 149, 0, 0.28);
      background: rgba(255, 149, 0, 0.1);
      color: #7a4a00;
      font-size: 13px;
      line-height: 1.55;
    }
    .toast {
      position: fixed;
      left: 50%;
      bottom: 24px;
      transform: translateX(-50%) translateY(20px);
      opacity: 0;
      transition: 0.2s ease;
      max-width: min(560px, calc(100vw - 32px));
      padding: 13px 16px;
      border-radius: 14px;
      color: #fff;
      background: rgba(29, 29, 31, 0.92);
      box-shadow: var(--shadow);
      pointer-events: none;
      font-size: 14px;
      line-height: 1.45;
    }
    .toast.show { opacity: 1; transform: translateX(-50%) translateY(0); }
    .toast.error { background: rgba(170, 31, 24, 0.95); }
    @media (max-width: 860px) {
      .grid, .status-grid, .row { grid-template-columns: 1fr; }
      .shell { width: min(100vw - 24px, 1120px); padding-top: 18px; }
    }
  </style>
</head>
<body>
  <main class="shell">
    <div class="nav">
      <a class="back" href="/admin">返回 Komari 后台</a>
      <span id="loadState" class="hint">正在读取配置...</span>
    </div>

    <header>
      <h1>全局运维报告</h1>
      <p class="subtitle">配置日报、周报、月报的触发周期、统计时区和 Telegram 可视化报告展示。日报统计配置时区里的前一完整自然日。</p>
    </header>

    <section class="grid">
      <form id="settingsForm" class="card">
        <div class="card-inner">
          <h2>报告计划</h2>
          <div class="switch-row">
            <div>
              <strong>启用全局运维报告</strong>
              <p>关闭后不会发送日报、周报和月报。</p>
            </div>
            <input id="enable" type="checkbox">
          </div>

          <div class="check-row">
            <div>
              <strong>日报</strong>
              <p>统计前一完整自然日。</p>
            </div>
            <input id="daily" type="checkbox">
          </div>
          <div class="check-row">
            <div>
              <strong>周报</strong>
              <p>每周一发送上一 ISO 周。</p>
            </div>
            <input id="weekly" type="checkbox">
          </div>
          <div class="check-row">
            <div>
              <strong>月报</strong>
              <p>每月 1 日发送上一自然月。</p>
            </div>
            <input id="monthly" type="checkbox">
          </div>

          <div class="row">
            <div>
              <label for="timezone">报告时区</label>
              <select id="timezone">
                <option value="UTC">UTC</option>
                <option value="UTC+8">UTC+8 固定偏移</option>
                <option value="UTC+0">UTC+0 固定偏移</option>
                <option value="UTC-5">UTC-5 固定偏移</option>
                <option value="UTC-8">UTC-8 固定偏移</option>
                <option value="Asia/Shanghai">Asia/Shanghai 中国标准时间</option>
                <option value="Asia/Hong_Kong">Asia/Hong_Kong 香港时间</option>
                <option value="Asia/Taipei">Asia/Taipei 台北时间</option>
                <option value="Asia/Tokyo">Asia/Tokyo 日本时间</option>
                <option value="Asia/Singapore">Asia/Singapore 新加坡时间</option>
                <option value="Europe/London">Europe/London 伦敦时间</option>
                <option value="Europe/Berlin">Europe/Berlin 柏林时间</option>
                <option value="America/Los_Angeles">America/Los_Angeles 洛杉矶时间</option>
                <option value="America/New_York">America/New_York 纽约时间</option>
              </select>
              <p class="hint">推荐优先选择城市时区；固定 UTC 偏移不包含夏令时规则。</p>
            </div>
            <div>
              <label for="sendHour">发送小时</label>
              <input id="sendHour" type="number" min="0" max="23" step="1">
            </div>
          </div>

          <div class="row">
            <div>
              <label for="topN">榜单数量 Top N</label>
              <input id="topN" type="number" min="1" max="20" step="1">
            </div>
            <div>
              <label for="testCadence">测试报告周期</label>
              <select id="testCadence">
                <option value="daily">日报</option>
                <option value="weekly">周报</option>
                <option value="monthly">月报</option>
              </select>
              <p id="testCadenceHint" class="hint">测试将发送日报，统计配置时区中的前一完整自然日。</p>
            </div>
          </div>

          <div class="actions">
            <button id="saveButton" class="primary" type="submit">保存配置</button>
            <button id="testButton" type="button">发送日报测试报告</button>
          </div>
          <p class="hint">测试报告周期只影响手动测试发送，不会修改上方日报、周报、月报的保存配置。</p>
        </div>
      </form>

      <aside class="card">
        <div class="card-inner">
          <h2>当前状态</h2>
          <div class="status-grid">
            <div id="channelStatus" class="status">
              <span>通知总开关</span>
              <strong>读取中</strong>
            </div>
            <div id="methodStatus" class="status">
              <span>投递方式</span>
              <strong>读取中</strong>
            </div>
            <div id="reportStatus" class="status">
              <span>报告状态</span>
              <strong>读取中</strong>
            </div>
          </div>
          <div class="last-list">
            <div class="last-item">
              <span>上次日报</span>
              <strong id="lastDaily">未发送</strong>
            </div>
            <div class="last-item">
              <span>上次周报</span>
              <strong id="lastWeekly">未发送</strong>
            </div>
            <div class="last-item">
              <span>上次月报</span>
              <strong id="lastMonthly">未发送</strong>
            </div>
          </div>
          <div class="notice">如果测试发送失败，请先确认通知总开关已开启，Delivery Method 已选择 JavaScript code，并且脚本内 Telegram Bot Token 与 Chat ID 已正确填写。</div>
        </div>
      </aside>
    </section>
  </main>
  <div id="toast" class="toast"></div>
  <script>
    const state = { settings: null, report: null };
    const $ = (id) => document.getElementById(id);

    function toast(message, isError) {
      const el = $("toast");
      el.textContent = message;
      el.className = "toast show" + (isError ? " error" : "");
      clearTimeout(window.__toastTimer);
      window.__toastTimer = setTimeout(() => { el.className = "toast"; }, 3600);
    }

    async function requestJSON(url, options) {
      const response = await fetch(url, Object.assign({ credentials: "same-origin" }, options || {}));
      const payload = await response.json().catch(() => ({ status: "error", message: "响应不是有效 JSON" }));
      if (!response.ok || payload.status !== "success") {
        throw new Error(payload.message || "请求失败");
      }
      return payload.data;
    }

    function formatTime(value) {
      if (!value || String(value).startsWith("0001")) return "未发送";
      const date = new Date(value);
      if (Number.isNaN(date.getTime())) return String(value);
      return date.toLocaleString();
    }

    function setStatusCard(id, value, mode) {
      const card = $(id);
      card.className = "status " + (mode || "");
      card.querySelector("strong").textContent = value;
    }

    function ensureTimezoneOption(value) {
      const select = $("timezone");
      if (!value) return;
      const exists = Array.from(select.options).some((option) => option.value === value);
      if (!exists) {
        const option = document.createElement("option");
        option.value = value;
        option.textContent = value + " 自定义";
        select.appendChild(option);
      }
    }

    function fillForm(report) {
      $("enable").checked = Boolean(report.enable);
      $("daily").checked = Boolean(report.daily);
      $("weekly").checked = Boolean(report.weekly);
      $("monthly").checked = Boolean(report.monthly);
      ensureTimezoneOption(report.timezone || "UTC");
      $("timezone").value = report.timezone || "UTC";
      $("sendHour").value = Number(report.send_hour ?? 9);
      $("topN").value = Number(report.top_n ?? 5);
      $("lastDaily").textContent = formatTime(report.last_daily_notified);
      $("lastWeekly").textContent = formatTime(report.last_weekly_notified);
      $("lastMonthly").textContent = formatTime(report.last_monthly_notified);
      setStatusCard("reportStatus", report.enable ? "已启用" : "未启用", report.enable ? "good" : "warn");
      updateTestCadenceHint();
    }

    function updateTestCadenceHint() {
      const cadence = $("testCadence").value;
      const copy = {
        daily: {
          label: "日报",
          hint: "测试将发送日报，统计配置时区中的前一完整自然日。"
        },
        weekly: {
          label: "周报",
          hint: "测试将发送周报，统计配置时区中的上一 ISO 周。"
        },
        monthly: {
          label: "月报",
          hint: "测试将发送月报，统计配置时区中的上一自然月。"
        }
      }[cadence] || {
        label: "报告",
        hint: "测试将发送所选周期报告。"
      };
      $("testCadenceHint").textContent = copy.hint;
      $("testButton").textContent = "发送" + copy.label + "测试报告";
    }

    function renderSettingsStatus(settings) {
      const enabled = Boolean(settings.notification_enabled);
      const method = String(settings.notification_method || "none");
      setStatusCard("channelStatus", enabled ? "已开启" : "未开启", enabled ? "good" : "bad");
      setStatusCard("methodStatus", method === "none" || method === "" ? "未配置" : method, method === "none" || method === "" ? "bad" : "good");
    }

    async function load() {
      $("loadState").textContent = "正在读取配置...";
      try {
        const [report, settings] = await Promise.all([
          requestJSON("/api/admin/notification/fleet-report"),
          requestJSON("/api/admin/settings/")
        ]);
        state.report = report;
        state.settings = settings;
        fillForm(report);
        renderSettingsStatus(settings);
        $("loadState").textContent = "配置已加载";
      } catch (error) {
        $("loadState").textContent = "读取失败";
        toast(error.message || String(error), true);
      }
    }

    function buildPayload() {
      return {
        enable: $("enable").checked,
        daily: $("daily").checked,
        weekly: $("weekly").checked,
        monthly: $("monthly").checked,
        timezone: $("timezone").value.trim() || "UTC",
        send_hour: Number($("sendHour").value),
        top_n: Number($("topN").value)
      };
    }

    $("settingsForm").addEventListener("submit", async (event) => {
      event.preventDefault();
      $("saveButton").disabled = true;
      try {
        const saved = await requestJSON("/api/admin/notification/fleet-report/edit", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify(buildPayload())
        });
        fillForm(saved);
        toast("全局运维报告配置已保存");
      } catch (error) {
        toast(error.message || String(error), true);
      } finally {
        $("saveButton").disabled = false;
      }
    });

    $("testButton").addEventListener("click", async () => {
      $("testButton").disabled = true;
      try {
        const result = await requestJSON("/api/admin/notification/fleet-report/test", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ cadence: $("testCadence").value })
        });
        const report = result.report || {};
        toast("测试报告已发送：" + (report.period_label || report.periodLabel || $("testCadence").value));
      } catch (error) {
        toast(error.message || String(error), true);
      } finally {
        $("testButton").disabled = false;
      }
    });

    $("testCadence").addEventListener("change", updateTestCadenceHint);

    load();
  </script>
</body>
</html>`
