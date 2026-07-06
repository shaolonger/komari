// Komari Javascript notification provider script for Telegram.
// Paste this file into notification provider "Javascript" -> "script".
// Do not paste it into the plain "notification_template" field.

// ==================== Telegram Config ====================
const TELEGRAM_BOT_TOKEN = ""; // required, e.g. "123456789:AA..."
const TELEGRAM_CHAT_ID = ""; // required, e.g. "123456789" or "-1001234567890"
const TELEGRAM_MESSAGE_THREAD_ID = ""; // optional, for Telegram forum topics
const PANEL_URL = ""; // optional, e.g. "https://status.example.com"

// ==================== Display Config ====================
const TIMEZONE_OFFSET_HOURS = 8;
const TIMEZONE_LABEL = "北京时间";
const HIDE_IP = true;
const SHOW_PANEL_BUTTON = true;
const SHOW_NODE_LINK_IN_BODY = true;
const MAX_TITLE_LENGTH = 80;
const MAX_TELEGRAM_TEXT_LENGTH = 3900;
const MAX_CLIENTS_SHOWN = 6;
const MAX_FLEET_RANKING_SECTIONS = 7;
const MAX_FLEET_RANK_ITEMS = 5;
const MAX_FLEET_ANOMALIES_SHOWN = 8;
const TELEGRAM_RETRY_ATTEMPTS = 3;
const TELEGRAM_RETRY_DELAY_MS = 800;

// ==================== Country Helpers ====================
const VALID_COUNTRY_CODES = {
  AC: 1, AD: 1, AE: 1, AF: 1, AG: 1, AI: 1, AL: 1, AM: 1, AO: 1, AQ: 1, AR: 1, AS: 1, AT: 1, AU: 1, AW: 1, AX: 1, AZ: 1,
  BA: 1, BB: 1, BD: 1, BE: 1, BF: 1, BG: 1, BH: 1, BI: 1, BJ: 1, BL: 1, BM: 1, BN: 1, BO: 1, BQ: 1, BR: 1, BS: 1, BT: 1,
  BW: 1, BY: 1, BZ: 1, CA: 1, CC: 1, CD: 1, CF: 1, CG: 1, CH: 1, CI: 1, CK: 1, CL: 1, CM: 1, CN: 1, CO: 1, CR: 1,
  CU: 1, CV: 1, CW: 1, CX: 1, CY: 1, CZ: 1, DE: 1, DJ: 1, DK: 1, DM: 1, DO: 1, DZ: 1, EC: 1, EE: 1, EG: 1, ER: 1,
  ES: 1, ET: 1, FI: 1, FJ: 1, FK: 1, FM: 1, FO: 1, FR: 1, GA: 1, GB: 1, GD: 1, GE: 1, GF: 1, GH: 1, GI: 1, GL: 1,
  GM: 1, GN: 1, GP: 1, GQ: 1, GR: 1, GT: 1, GU: 1, GW: 1, GY: 1, HK: 1, HN: 1, HR: 1, HT: 1, HU: 1, ID: 1, IE: 1,
  IL: 1, IM: 1, IN: 1, IO: 1, IQ: 1, IR: 1, IS: 1, IT: 1, JE: 1, JM: 1, JO: 1, JP: 1, KE: 1, KG: 1, KH: 1, KI: 1,
  KM: 1, KN: 1, KP: 1, KR: 1, KW: 1, KY: 1, KZ: 1, LA: 1, LB: 1, LC: 1, LI: 1, LK: 1, LR: 1, LS: 1, LT: 1, LU: 1,
  LV: 1, LY: 1, MA: 1, MC: 1, MD: 1, ME: 1, MG: 1, MH: 1, MK: 1, ML: 1, MM: 1, MN: 1, MO: 1, MQ: 1, MR: 1, MS: 1,
  MT: 1, MU: 1, MV: 1, MW: 1, MX: 1, MY: 1, MZ: 1, NA: 1, NC: 1, NE: 1, NF: 1, NG: 1, NI: 1, NL: 1, NO: 1, NP: 1,
  NR: 1, NU: 1, NZ: 1, OM: 1, PA: 1, PE: 1, PF: 1, PG: 1, PH: 1, PK: 1, PL: 1, PM: 1, PR: 1, PS: 1, PT: 1, PW: 1,
  PY: 1, QA: 1, RE: 1, RO: 1, RS: 1, RU: 1, RW: 1, SA: 1, SB: 1, SC: 1, SD: 1, SE: 1, SG: 1, SI: 1, SK: 1, SL: 1,
  SM: 1, SN: 1, SO: 1, SR: 1, SS: 1, ST: 1, SV: 1, SX: 1, SY: 1, SZ: 1, TC: 1, TD: 1, TG: 1, TH: 1, TJ: 1, TK: 1,
  TL: 1, TM: 1, TN: 1, TO: 1, TR: 1, TT: 1, TV: 1, TW: 1, TZ: 1, UA: 1, UG: 1, US: 1, UY: 1, UZ: 1, VA: 1, VC: 1,
  VE: 1, VG: 1, VI: 1, VN: 1, VU: 1, WF: 1, WS: 1, XK: 1, YE: 1, YT: 1, ZA: 1, ZM: 1, ZW: 1
};

const COUNTRY_ALIASES = {
  "中国": "CN", "大陆": "CN", "北京": "CN", "上海": "CN", "广州": "CN", "深圳": "CN", "杭州": "CN",
  "香港": "HK", "澳门": "MO", "台湾": "TW",
  "美国": "US", "洛杉矶": "US", "纽约": "US", "芝加哥": "US", "西雅图": "US", "达拉斯": "US", "圣何塞": "US", "硅谷": "US",
  "usa": "US", "united states": "US", "america": "US", "los angeles": "US", "new york": "US", "chicago": "US", "seattle": "US", "dallas": "US", "san jose": "US",
  "加拿大": "CA", "多伦多": "CA", "温哥华": "CA", "蒙特利尔": "CA", "canada": "CA", "toronto": "CA", "vancouver": "CA", "montreal": "CA",
  "日本": "JP", "东京": "JP", "大阪": "JP", "japan": "JP", "tokyo": "JP", "osaka": "JP",
  "韩国": "KR", "首尔": "KR", "korea": "KR", "south korea": "KR", "seoul": "KR",
  "新加坡": "SG", "singapore": "SG",
  "德国": "DE", "法兰克福": "DE", "柏林": "DE", "germany": "DE", "deutschland": "DE", "frankfurt": "DE", "berlin": "DE",
  "英国": "GB", "伦敦": "GB", "united kingdom": "GB", "britain": "GB", "london": "GB",
  "法国": "FR", "巴黎": "FR", "france": "FR", "paris": "FR",
  "荷兰": "NL", "阿姆斯特丹": "NL", "netherlands": "NL", "holland": "NL", "amsterdam": "NL",
  "俄罗斯": "RU", "莫斯科": "RU", "russia": "RU", "moscow": "RU",
  "乌克兰": "UA", "ukraine": "UA",
  "澳大利亚": "AU", "澳洲": "AU", "悉尼": "AU", "墨尔本": "AU", "australia": "AU", "sydney": "AU", "melbourne": "AU",
  "印度": "IN", "孟买": "IN", "德里": "IN", "india": "IN", "mumbai": "IN", "delhi": "IN",
  "泰国": "TH", "曼谷": "TH", "thailand": "TH", "bangkok": "TH",
  "越南": "VN", "河内": "VN", "胡志明": "VN", "vietnam": "VN", "hanoi": "VN", "ho chi minh": "VN",
  "马来西亚": "MY", "吉隆坡": "MY", "malaysia": "MY", "kuala lumpur": "MY",
  "菲律宾": "PH", "马尼拉": "PH", "philippines": "PH", "manila": "PH",
  "印尼": "ID", "印度尼西亚": "ID", "雅加达": "ID", "indonesia": "ID", "jakarta": "ID",
  "土耳其": "TR", "伊斯坦布尔": "TR", "turkey": "TR", "istanbul": "TR",
  "阿联酋": "AE", "迪拜": "AE", "uae": "AE", "dubai": "AE",
  "巴西": "BR", "圣保罗": "BR", "brazil": "BR", "sao paulo": "BR",
  "墨西哥": "MX", "mexico": "MX", "阿根廷": "AR", "argentina": "AR", "智利": "CL", "chile": "CL",
  "南非": "ZA", "south africa": "ZA", "意大利": "IT", "米兰": "IT", "罗马": "IT", "italy": "IT",
  "西班牙": "ES", "马德里": "ES", "spain": "ES", "瑞士": "CH", "苏黎世": "CH", "switzerland": "CH"
};

// ==================== Generic Helpers ====================
function asString(value, fallback) {
  if (value === undefined || value === null) return fallback || "";
  return String(value);
}

function firstNonEmpty(values, fallback) {
  for (let i = 0; i < values.length; i++) {
    if (values[i] !== undefined && values[i] !== null && String(values[i]).trim() !== "") {
      return values[i];
    }
  }
  return fallback;
}

function truncateText(text, maxLength) {
  text = asString(text, "");
  if (text.length <= maxLength) return text;
  return text.slice(0, Math.max(0, maxLength - 16)) + "\n...(已截断)";
}

function htmlEscape(text) {
  return asString(text, "")
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;");
}

function normalizePanelUrl() {
  return asString(PANEL_URL, "").replace(/\/+$/, "");
}

function getInstanceUrl(uuid) {
  const base = normalizePanelUrl();
  if (!base || !uuid) return base;
  return base + "/instance/" + encodeURIComponent(uuid);
}

function pad2(number) {
  number = Number(number || 0);
  return number < 10 ? "0" + number : String(number);
}

function parseDateValue(timeValue) {
  if (!timeValue || String(timeValue).indexOf("0001") === 0) return null;
  const date = new Date(String(timeValue).replace(/\.\d+Z$/, "Z"));
  return isNaN(date.getTime()) ? null : date;
}

function formatLocalTime(timeValue) {
  const date = parseDateValue(timeValue);
  if (!date) return "未知";

  const shifted = new Date(date.getTime() + TIMEZONE_OFFSET_HOURS * 60 * 60 * 1000);
  return shifted.getUTCFullYear() + "-" + pad2(shifted.getUTCMonth() + 1) + "-" + pad2(shifted.getUTCDate()) +
    " " + pad2(shifted.getUTCHours()) + ":" + pad2(shifted.getUTCMinutes()) + ":" + pad2(shifted.getUTCSeconds());
}

function displayTimeLabel() {
  return TIMEZONE_LABEL || "通知时间";
}

function formatBytes(bytes, zeroText) {
  const n = Number(bytes || 0);
  if (!n || n <= 0) return zeroText || "0 B";
  const units = ["B", "KB", "MB", "GB", "TB", "PB"];
  let value = n;
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) {
    value = value / 1024;
    unit++;
  }
  const digits = value >= 100 || unit === 0 ? 0 : value >= 10 ? 1 : 2;
  return value.toFixed(digits) + " " + units[unit];
}

function formatMemory(bytes) {
  const n = Number(bytes || 0);
  if (!n || n <= 0) return "0";
  const gb = n / Math.pow(1024, 3);
  return gb < 1 ? Math.round(gb * 1024) + "MB" : (gb >= 10 ? Math.round(gb) : gb.toFixed(1)) + "G";
}

function formatTrafficLimit(bytes) {
  const n = Number(bytes || 0);
  if (!n || n <= 0) return "无限制";
  return formatBytes(n, "无限制");
}

function formatPercentText(value, fallback) {
  const n = Number(value);
  if (isNaN(n)) return fallback || "0.0%";
  return n.toFixed(1) + "%";
}

function visualBar(percent, width) {
  width = width || 12;
  let n = Number(percent || 0);
  if (isNaN(n)) n = 0;
  if (n < 0) n = 0;
  if (n > 100) n = 100;
  const filled = Math.round(n / 100 * width);
  return "▰".repeat(filled) + "▱".repeat(width - filled);
}

function asArray(value) {
  return Array.isArray(value) ? value : [];
}

function maskIP(ip) {
  ip = asString(ip, "").trim();
  if (!ip) return "未知";
  if (!HIDE_IP) return ip;

  if (ip.indexOf(".") >= 0) {
    const parts = ip.split(".");
    if (parts.length === 4) return parts[0] + "." + parts[1] + ".xxx.xxx";
  }

  if (ip.indexOf(":") >= 0) {
    const parts6 = ip.split(":").filter(function (part) { return part !== ""; });
    if (parts6.length >= 2) return parts6.slice(0, 3).join(":") + ":xxxx:xxxx";
  }
  return "未知";
}

function countryCodeToFlag(code) {
  code = asString(code, "").toUpperCase().trim();
  if (!code || code.length !== 2 || !VALID_COUNTRY_CODES[code]) return "";
  try {
    return String.fromCodePoint(127397 + code.charCodeAt(0)) +
      String.fromCodePoint(127397 + code.charCodeAt(1));
  } catch (error) {
    return "";
  }
}

function hasFlagEmoji(text) {
  return /[\uD83C][\uDDE6-\uDDFF][\uD83C][\uDDE6-\uDDFF]/.test(asString(text, ""));
}

function getCountryCodeFromClient(client) {
  if (!client) return "";
  const directFields = [
    client.country_code, client.countryCode, client.region_code, client.regionCode,
    client.iso2, client.cc, client.country_code2, client.countryCode2
  ];
  for (let i = 0; i < directFields.length; i++) {
    const code = asString(directFields[i], "").trim().toUpperCase();
    if (VALID_COUNTRY_CODES[code]) return code;
  }

  const text = [
    client.name, client.region, client.country, client.location,
    client.remark, client.public_remark, client.description, client.hostname, client.host
  ].filter(Boolean).join(" ").toLowerCase();

  for (const key in COUNTRY_ALIASES) {
    if (text.indexOf(key.toLowerCase()) >= 0) return COUNTRY_ALIASES[key];
  }

  const tokens = text.toUpperCase().split(/[^A-Z]/).filter(function (token) {
    return token && token.length === 2;
  });
  for (let j = 0; j < tokens.length; j++) {
    if (VALID_COUNTRY_CODES[tokens[j]]) return tokens[j];
  }
  return "";
}

function getFlag(client) {
  if (!client) return "";
  if (hasFlagEmoji(client.region) || hasFlagEmoji(client.name)) return "";
  const code = getCountryCodeFromClient(client);
  return code ? countryCodeToFlag(code) : "";
}

function billingCycleLabel(cycleValue) {
  const raw = asString(cycleValue, "").trim();
  if (!raw || raw === "0") return "";
  const lower = raw.toLowerCase();
  const map = { month: "/月", monthly: "/月", year: "/年", yearly: "/年", quarter: "/季", weekly: "/周", week: "/周" };
  if (map[lower]) return map[lower];
  if (!isNaN(Number(raw))) return "/" + raw + "天";
  return "/" + raw;
}

function formatBilling(client) {
  const price = client && client.price;
  if (price === undefined || price === null || price === -1 || String(price).toLowerCase() === "free") {
    return "免费";
  }
  return asString(client.currency, "$") + price + billingCycleLabel(firstNonEmpty([client.billing_cycle, client.billingCycle], ""));
}

function trafficTypeLabel(type) {
  type = asString(type, "max").toLowerCase().trim();
  const map = { sum: "总和", max: "取最大", min: "取最小", up: "仅上传", down: "仅下载" };
  return map[type] || type;
}

function translateMessage(message) {
  let msg = asString(message, "").trim();
  if (!msg) return "";

  const exactMap = {
    "client is offline": "节点已离线",
    "client is online": "节点已恢复在线",
    "server is offline": "服务器已离线",
    "server is online": "服务器已恢复在线",
    "node is offline": "节点已离线",
    "node is online": "节点已恢复在线",
    "heartbeat timeout": "心跳超时",
    "connection timeout": "连接超时",
    "request timeout": "请求超时",
    "ping timeout": "Ping 超时",
    "test message": "测试消息",
    "test notification": "测试通知",
    "cpu usage is too high": "CPU 使用率过高",
    "memory usage is too high": "内存使用率过高",
    "ram usage is too high": "内存使用率过高",
    "disk usage is too high": "磁盘使用率过高",
    "load is too high": "系统负载过高",
    "service expired": "服务已到期",
    "service will expire": "服务即将到期"
  };
  const lower = msg.toLowerCase();
  if (exactMap[lower]) return exactMap[lower];

  const rules = [
    [/\bclient is offline\b/gi, "节点已离线"], [/\bclient is online\b/gi, "节点已恢复在线"],
    [/\bserver is offline\b/gi, "服务器已离线"], [/\bserver is online\b/gi, "服务器已恢复在线"],
    [/\bnode is offline\b/gi, "节点已离线"], [/\bnode is online\b/gi, "节点已恢复在线"],
    [/\bheartbeat timeout\b/gi, "心跳超时"], [/\bconnection timeout\b/gi, "连接超时"],
    [/\brequest timeout\b/gi, "请求超时"], [/\bresponse timeout\b/gi, "响应超时"],
    [/\bping timeout\b/gi, "Ping 超时"], [/\bcpu\b/gi, "CPU"], [/\bmemory\b/gi, "内存"],
    [/\bram\b/gi, "内存"], [/\bdisk\b/gi, "磁盘"], [/\btraffic\b/gi, "流量"],
    [/\bnetwork\b/gi, "网络"], [/\boffline\b/gi, "离线"], [/\bonline\b/gi, "在线"],
    [/\balert\b/gi, "告警"], [/\bwarning\b/gi, "警告"], [/\bcritical\b/gi, "严重"],
    [/\berror\b/gi, "错误"], [/\bfailed\b/gi, "失败"], [/\bfailure\b/gi, "故障"],
    [/\bsuccess\b/gi, "成功"], [/\btimeout\b/gi, "超时"], [/\bexpire(d)?\b/gi, "到期"],
    [/\brenew\b/gi, "续费"], [/\btest\b/gi, "测试"], [/\busage\b/gi, "使用率"], [/\bhigh\b/gi, "过高"]
  ];
  for (let i = 0; i < rules.length; i++) {
    msg = msg.replace(rules[i][0], rules[i][1]);
  }
  return msg;
}

function parseTrafficAlert(message) {
  const text = asString(message, "");
  const match = text.match(/used\s+(\d+)%\s+\(([^/]+)\/\s*([^)]+)\),?\s*type=([a-z]+)/i);
  if (!match) return null;
  return {
    percent: match[1] + "%",
    used: match[2].trim(),
    limit: match[3].trim(),
    type: trafficTypeLabel(match[4])
  };
}

// ==================== Event Formatters ====================
const EVENT_META = {
  online: { icon: "🟢", title: "服务器上线", severity: "正常", accent: "恢复" },
  offline: { icon: "🔴", title: "服务器离线", severity: "异常", accent: "需要关注" },
  alert: { icon: "⚠️", title: "监控告警", severity: "警告", accent: "资源异常" },
  traffic: { icon: "📶", title: "流量告警", severity: "警告", accent: "额度接近阈值" },
  renew: { icon: "💰", title: "续费通知", severity: "提醒", accent: "账单变更" },
  expire: { icon: "🚨", title: "到期预警", severity: "重要", accent: "请及时处理" },
  expired: { icon: "🚨", title: "服务到期", severity: "重要", accent: "已到期" },
  login: { icon: "🔐", title: "登录通知", severity: "提醒", accent: "账户活动" },
  test: { icon: "🧪", title: "测试通知", severity: "测试", accent: "配置验证" },
  recover: { icon: "🟢", title: "告警恢复", severity: "正常", accent: "已恢复" },
  recovered: { icon: "🟢", title: "告警恢复", severity: "正常", accent: "已恢复" },
  report: { icon: "📊", title: "流量定时报告", severity: "报告", accent: "周期汇总" },
  fleet_report: { icon: "📊", title: "全局运维报告", severity: "报告", accent: "全局健康与异常排行" },
  notice: { icon: "📌", title: "系统通知", severity: "通知", accent: "常规事件" }
};

function normalizeEventName(event) {
  let name = asString(event && event.event, "").toLowerCase().trim();
  const message = asString(event && event.message, "");
  const data = eventData(event);
  if (data && data.kind === "fleet_report") return "fleet_report";
  if (name.indexOf("fleetreport") >= 0 || name.indexOf("fleet_report") >= 0 || message.indexOf("全局运维报告") >= 0) return "fleet_report";
  if (name.indexOf("report") >= 0 || message.indexOf("流量报告") >= 0 || message.toLowerCase().indexOf("traffic report") >= 0) return "report";
  if (name.indexOf("offline") >= 0) return "offline";
  if (name.indexOf("online") >= 0) return "online";
  if (name.indexOf("traffic") >= 0) return "traffic";
  if (name.indexOf("alert") >= 0 || name.indexOf("load") >= 0) return "alert";
  if (name.indexOf("renew") >= 0) return "renew";
  if (name.indexOf("expire") >= 0) return "expire";
  if (name.indexOf("login") >= 0) return "login";
  if (name.indexOf("test") >= 0) return "test";
  if (name.indexOf("recover") >= 0) return "recover";
  return name || "notice";
}

function eventData(event) {
  if (!event) return {};
  return event.data || event.Data || {};
}

function normalizeClients(event) {
  if (!event) return [];
  if (Array.isArray(event.clients)) return event.clients;
  if (Array.isArray(event.Clients)) return event.Clients;
  if (event.client) return [event.client];
  if (event.Client) return [event.Client];
  return [];
}

function compactClientName(client, index) {
  client = client || {};
  const uuid = firstNonEmpty([client.uuid, client.UUID], "");
  const name = firstNonEmpty([client.name, uuid], "未知节点");
  const flag = getFlag(client);
  const region = asString(client.region, "");
  return (index + 1) + ". " + (flag ? flag + " " : "") + name + (region ? " [" + region + "]" : "");
}

function formatFullClientCard(client) {
  client = client || {};
  const uuid = firstNonEmpty([client.uuid, client.UUID], "");
  const name = firstNonEmpty([client.name, uuid], "未知节点");
  const flag = getFlag(client);
  const region = asString(client.region, "");
  const provider = asString(client.provider, "");
  const role = asString(client.business_role, "");
  const group = asString(client.group, "");

  const lines = [];
  lines.push("🖥️ 服务器: " + (flag ? flag + " " : "") + name + (region ? " [" + region + "]" : ""));

  const labels = [];
  if (provider) labels.push(provider);
  if (role) labels.push(role);
  if (group) labels.push(group);
  if (labels.length > 0) lines.push("🏷️ 标签: " + labels.join(" / "));

  const cpuCores = firstNonEmpty([client.cpu_cores, client.cpuCores], 0);
  const mem = formatMemory(firstNonEmpty([client.mem_total, client.memTotal], 0));
  const swap = formatMemory(firstNonEmpty([client.swap_total, client.swapTotal], 0));
  const disk = formatMemory(firstNonEmpty([client.disk_total, client.diskTotal], 0));
  lines.push("⚙️ 配置: " + cpuCores + "C / " + mem + (swap !== "0" ? "+" + swap : "") + " / " + disk);

  const osParts = [client.os, client.arch, client.version].filter(Boolean);
  if (osParts.length > 0) lines.push("🧩 系统: " + osParts.join(" / "));

  const ipv4 = maskIP(firstNonEmpty([client.ipv4, client.IPv4], ""));
  const ipv6 = maskIP(firstNonEmpty([client.ipv6, client.IPv6], ""));
  if (ipv4 !== "未知") lines.push("🌐 IPv4: " + ipv4);
  if (ipv6 !== "未知") lines.push("🌐 IPv6: " + ipv6);

  const trafficLimit = firstNonEmpty([client.traffic_limit, client.trafficLimit], 0);
  const trafficType = firstNonEmpty([client.traffic_limit_type, client.trafficLimitType], "max");
  lines.push("📶 流量: " + formatTrafficLimit(trafficLimit) + " (" + trafficTypeLabel(trafficType) + ")");
  lines.push("💰 账单: " + formatBilling(client));

  const expiredAt = firstNonEmpty([client.expired_at, client.expiredAt], "");
  if (expiredAt) lines.push("⏳ 到期: " + formatLocalTime(expiredAt));

  if (SHOW_NODE_LINK_IN_BODY) {
    const url = getInstanceUrl(uuid);
    if (url) lines.push("🔗 面板: " + url);
  }
  return lines.join("\n");
}

function formatClientsSection(clients) {
  if (!clients || clients.length === 0) return "🖥️ 范围: 全局系统级事件";
  if (clients.length === 1) return formatFullClientCard(clients[0]);

  const lines = ["📦 关联节点: " + clients.length + " 台"];
  const shown = Math.min(clients.length, MAX_CLIENTS_SHOWN);
  for (let i = 0; i < shown; i++) {
    lines.push(compactClientName(clients[i], i));
  }
  if (clients.length > shown) {
    lines.push("📌 仅展示前 " + shown + " 台，其余 " + (clients.length - shown) + " 台请前往面板查看。");
  }
  return lines.join("\n");
}

function buildEventMessage(event, meta, clients) {
  const lines = [];
  lines.push(formatClientsSection(clients));
  lines.push("");
  lines.push("📊 事件级别: " + meta.severity);
  lines.push("🎯 事件重点: " + meta.accent);
  lines.push("🕒 " + displayTimeLabel() + ": " + formatLocalTime(event.time || event.Time));

  const rawMessage = firstNonEmpty([event.message, event.Message], "");
  const translated = translateMessage(rawMessage);
  const trafficAlert = parseTrafficAlert(rawMessage);

  if (trafficAlert) {
    lines.push("");
    lines.push("📶 流量详情:");
    lines.push("• 当前用量: " + trafficAlert.used);
    lines.push("• 流量限额: " + trafficAlert.limit);
    lines.push("• 使用比例: " + trafficAlert.percent);
    lines.push("• 统计方式: " + trafficAlert.type);
  }

  if (translated) {
    lines.push("");
    lines.push("📄 详细描述:");
    lines.push(translated);
  }
  return lines.join("\n");
}

function parseKeyValueLines(message) {
  const details = {};
  const lines = asString(message, "").split(/\r?\n/);
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i].trim();
    if (!line) continue;
    const match = line.match(/^([^:：]+)[:：]\s*(.*)$/);
    if (!match) continue;
    details[match[1].trim()] = match[2].trim();
  }
  return details;
}

function detailValue(details, keys, fallback) {
  for (let i = 0; i < keys.length; i++) {
    if (details[keys[i]]) return details[keys[i]];
  }
  return fallback === undefined ? "未知" : fallback;
}

function buildReportEventMessage(event, meta, clients) {
  const rawMessage = firstNonEmpty([event.message, event.Message], "");
  const details = parseKeyValueLines(rawMessage);
  const lines = [];
  lines.push(formatClientsSection(clients));
  lines.push("");
  lines.push("📊 报告类型: " + detailValue(details, ["周期"], meta.title));
  lines.push("🗓️ 时间范围: " + detailValue(details, ["时间范围"], "未知"));
  lines.push("🕒 " + displayTimeLabel() + ": " + formatLocalTime(event.time || event.Time));
  lines.push("");
  lines.push("📈 流量概览:");
  lines.push("• 上行: " + detailValue(details, ["上行"], "0 B"));
  lines.push("• 下行: " + detailValue(details, ["下行"], "0 B"));
  lines.push("• 总计: " + detailValue(details, ["总计"], "0 B"));
  lines.push("• 平均速率: " + detailValue(details, ["平均速率"], "0 B/s"));
  lines.push("• 峰值速率: " + detailValue(details, ["峰值速率"], "0 B/s"));
  lines.push("");
  lines.push("🧪 数据可信度:");
  lines.push("• 样本数: " + detailValue(details, ["样本数"], "0"));
  lines.push("• 覆盖率: " + detailValue(details, ["覆盖率"], "0.0%"));
  lines.push("• 数据质量: " + detailValue(details, ["数据质量"], "未知"));

  const usage = detailValue(details, ["额度用量"], "");
  const remaining = detailValue(details, ["剩余额度"], "");
  const type = detailValue(details, ["额度类型"], "");
  if (usage || remaining || type) {
    lines.push("");
    lines.push("📦 套餐额度:");
    if (type) lines.push("• 统计方式: " + type);
    if (usage) lines.push("• 当前用量: " + usage);
    if (remaining) lines.push("• 剩余额度: " + remaining);
  }

  const resets = detailValue(details, ["计数器重置"], "");
  const sampleRange = detailValue(details, ["样本范围"], "");
  if (resets || sampleRange) {
    lines.push("");
    lines.push("🔎 采样备注:");
    if (sampleRange) lines.push("• 样本范围: " + sampleRange);
    if (resets) lines.push("• 计数器重置: " + resets);
  }

  if (Object.keys(details).length === 0 && rawMessage) {
    lines.push("");
    lines.push("📄 原始报告:");
    lines.push(rawMessage);
  }
  return lines.join("\n");
}

function severityBadge(severity) {
  severity = asString(severity, "").toLowerCase();
  if (severity === "critical") return "🔴 严重";
  if (severity === "warning") return "🟡 警告";
  return "🔵 提示";
}

function formatFleetReportEventMessage(event, meta) {
  const data = eventData(event);
  const summary = data.summary || data.Summary || {};
  const rankings = asArray(data.rankings || data.Rankings);
  const anomalies = asArray(data.anomalies || data.Anomalies);
  const recommendations = asArray(data.recommendations || data.Recommendations);
  const rawMessage = firstNonEmpty([event.message, event.Message], "");

  if (!data || data.kind !== "fleet_report") {
    return rawMessage || "收到全局运维报告，但当前事件没有结构化报告数据。";
  }

  const lines = [];
  const cadenceLabel = firstNonEmpty([data.cadence_label, data.CadenceLabel], meta.title);
  const periodLabel = firstNonEmpty([data.period_label, data.PeriodLabel], "未知周期");
  const timezone = firstNonEmpty([data.timezone, data.Timezone, event.timezone, event.Timezone], TIMEZONE_LABEL);
  const generatedAt = firstNonEmpty([data.generated_at, data.GeneratedAt], formatLocalTime(event.time || event.Time));
  const healthScore = Number(firstNonEmpty([summary.health_score, summary.HealthScore], 0));

  lines.push("🧭 " + cadenceLabel + " · " + periodLabel);
  lines.push("🌐 时区: " + timezone + " · 生成: " + generatedAt);
  lines.push("");
  lines.push("🏆 健康分: " + healthScore + "/100  " + visualBar(healthScore, 12));
  lines.push("🖥️ 节点: " + firstNonEmpty([summary.total_nodes, summary.TotalNodes], 0) +
    " 台 / 有数据 " + firstNonEmpty([summary.report_nodes, summary.ReportNodes], 0) +
    " / 无数据 " + firstNonEmpty([summary.no_data_nodes, summary.NoDataNodes], 0));
  lines.push("🚨 异常: " + firstNonEmpty([summary.anomaly_nodes, summary.AnomalyNodes], 0) +
    " 台 / 严重 " + firstNonEmpty([summary.critical_anomalies, summary.CriticalAnomalies], 0) +
    " / 警告 " + firstNonEmpty([summary.warning_anomalies, summary.WarningAnomalies], 0));
  lines.push("🧪 覆盖率: " + formatPercentText(firstNonEmpty([summary.data_coverage, summary.DataCoverage], 0)) +
    "  " + visualBar(firstNonEmpty([summary.data_coverage, summary.DataCoverage], 0), 12));
  lines.push("📶 总流量: " + firstNonEmpty([summary.total_traffic_text, summary.TotalTrafficText], "0 B"));

  const avgPingP95 = Number(firstNonEmpty([summary.avg_ping_p95, summary.AvgPingP95], 0));
  const avgPingLoss = Number(firstNonEmpty([summary.avg_ping_loss, summary.AvgPingLoss], 0));
  if (avgPingP95 > 0 || avgPingLoss > 0) {
    lines.push("📡 Ping: P95 均值 " + avgPingP95.toFixed(0) + " ms / 丢包均值 " + formatPercentText(avgPingLoss));
  }

  if (rankings.length > 0) {
    lines.push("");
    lines.push("📊 关键榜单");
    const sectionCount = Math.min(rankings.length, MAX_FLEET_RANKING_SECTIONS);
    for (let i = 0; i < sectionCount; i++) {
      const ranking = rankings[i] || {};
      const items = asArray(ranking.items || ranking.Items);
      if (items.length === 0) continue;
      lines.push("");
      lines.push("▸ " + firstNonEmpty([ranking.title, ranking.Title], "排行"));
      const itemCount = Math.min(items.length, MAX_FLEET_RANK_ITEMS);
      for (let j = 0; j < itemCount; j++) {
        const item = items[j] || {};
        const name = firstNonEmpty([item.name, item.Name, item.uuid, item.UUID], "未知节点");
        const value = firstNonEmpty([item.display_value, item.DisplayValue], "");
        const percent = firstNonEmpty([item.percent, item.Percent], 0);
        const detail = firstNonEmpty([item.detail, item.Detail], "");
        lines.push("#" + firstNonEmpty([item.rank, item.Rank], j + 1) + " " + name + "  " + value);
        lines.push("   " + visualBar(percent, 10) + (detail ? "  " + detail : ""));
      }
    }
  }

  if (anomalies.length > 0) {
    lines.push("");
    lines.push("🚨 异常摘要");
    const limit = Math.min(anomalies.length, MAX_FLEET_ANOMALIES_SHOWN);
    for (let k = 0; k < limit; k++) {
      const anomaly = anomalies[k] || {};
      const name = firstNonEmpty([anomaly.name, anomaly.Name], "全局");
      const title = firstNonEmpty([anomaly.title, anomaly.Title], "异常");
      const detail = firstNonEmpty([anomaly.detail, anomaly.Detail], "");
      lines.push(severityBadge(firstNonEmpty([anomaly.severity, anomaly.Severity], "")) + " " + name + " · " + title);
      if (detail) lines.push("   " + detail);
    }
    if (anomalies.length > limit) {
      lines.push("… 还有 " + (anomalies.length - limit) + " 条异常，请进入面板查看。");
    }
  }

  if (recommendations.length > 0) {
    lines.push("");
    lines.push("✅ 建议动作");
    for (let r = 0; r < Math.min(recommendations.length, 4); r++) {
      lines.push("• " + recommendations[r]);
    }
  }
  return lines.join("\n");
}

function splitTelegramText(text) {
  text = asString(text, "");
  if (text.length <= MAX_TELEGRAM_TEXT_LENGTH) return [text];

  const chunks = [];
  let rest = text;
  while (rest.length > MAX_TELEGRAM_TEXT_LENGTH) {
    let cutAt = rest.lastIndexOf("\n", MAX_TELEGRAM_TEXT_LENGTH);
    if (cutAt < 1000) cutAt = MAX_TELEGRAM_TEXT_LENGTH;
    chunks.push(rest.slice(0, cutAt));
    rest = rest.slice(cutAt).replace(/^\n+/, "");
  }
  if (rest) chunks.push(rest);
  return chunks;
}

function telegramEndpoint() {
  return "https://api.telegram.org/bot" + TELEGRAM_BOT_TOKEN + "/sendMessage";
}

function sleep(ms) {
  return new Promise(function (resolve) {
    setTimeout(resolve, ms);
  });
}

function telegramButtonMarkup(instanceId) {
  if (!SHOW_PANEL_BUTTON) return null;
  const url = instanceId ? getInstanceUrl(instanceId) : normalizePanelUrl();
  if (!url) return null;
  return {
    inline_keyboard: [[
      { text: instanceId ? "打开节点面板" : "打开 Komari 面板", url: url }
    ]]
  };
}

async function sendTelegramText(htmlText, instanceId, attachButton) {
  const body = {
    chat_id: TELEGRAM_CHAT_ID,
    text: htmlText,
    parse_mode: "HTML",
    disable_web_page_preview: true
  };
  if (TELEGRAM_MESSAGE_THREAD_ID) {
    body.message_thread_id = Number(TELEGRAM_MESSAGE_THREAD_ID);
  }
  if (attachButton) {
    const markup = telegramButtonMarkup(instanceId);
    if (markup) body.reply_markup = markup;
  }

  let lastError = null;
  for (let attempt = 1; attempt <= TELEGRAM_RETRY_ATTEMPTS; attempt++) {
    try {
      const response = await fetch(telegramEndpoint(), {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body)
      });

      let result = {};
      try {
        result = await response.json();
      } catch (error) {
        result = { ok: false, description: "Telegram returned non-JSON response" };
      }
      if (response.ok && result.ok) return true;
      lastError = JSON.stringify(result);
    } catch (error) {
      lastError = asString(error && error.message, error);
    }
    if (attempt < TELEGRAM_RETRY_ATTEMPTS) {
      await sleep(TELEGRAM_RETRY_DELAY_MS * attempt);
    }
  }
  console.error("Telegram sendMessage failed:", lastError);
  return false;
}

// Komari requires this function. It is used when sending plain text messages
// and also by sendEvent after formatting a structured event.
async function sendMessage(message, title, instanceId) {
  if (!TELEGRAM_BOT_TOKEN || !TELEGRAM_CHAT_ID) {
    console.error("TELEGRAM_BOT_TOKEN or TELEGRAM_CHAT_ID is empty.");
    return false;
  }

  const safeTitle = htmlEscape(truncateText(title || "Komari 通知", MAX_TITLE_LENGTH));
  const chunks = splitTelegramText(message || "");
  for (let i = 0; i < chunks.length; i++) {
    const suffix = chunks.length > 1 ? " (" + (i + 1) + "/" + chunks.length + ")" : "";
    const htmlText = "<b>" + safeTitle + suffix + "</b>\n\n" + htmlEscape(chunks[i]);
    const ok = await sendTelegramText(htmlText, instanceId, i === 0);
    if (!ok) return false;
  }
  return true;
}

// Komari uses this function for structured notification events.
async function sendEvent(event) {
  event = event || {};
  try {
    const eventName = normalizeEventName(event);
    const meta = EVENT_META[eventName] || EVENT_META.notice;
    const clients = normalizeClients(event);
    const instanceId = clients.length === 1 ? firstNonEmpty([clients[0].uuid, clients[0].UUID], null) : null;
    const title = meta.icon + " " + meta.title;
    const message = eventName === "fleet_report"
      ? formatFleetReportEventMessage(event, meta)
      : eventName === "report"
        ? buildReportEventMessage(event, meta, clients)
        : buildEventMessage(event, meta, clients);
    return await sendMessage(message, title, instanceId);
  } catch (error) {
    console.error("sendEvent failed:", error);
    const fallbackTitle = "⚠️ Komari 通知脚本异常";
    const fallbackMessage =
      "事件: " + asString(event && event.event, "未知") + "\n" +
      "时间: " + formatLocalTime(event && (event.time || event.Time)) + "\n" +
      "错误: " + asString(error && error.message, error);
    try {
      return await sendMessage(fallbackMessage, fallbackTitle);
    } catch (fallbackError) {
      console.error("fallback notification failed:", fallbackError);
      return false;
    }
  }
}
