// Komari Javascript notification provider script for MeoW push.
// Paste this into notification provider "Javascript" -> "script", not into
// the plain "notification_template" message template field.

// ==================== Push Config ====================
const MEOW_NICKNAME = ""; // required, e.g. "your-meow-nickname"
const PANEL_URL = ""; // optional, e.g. "https://komari.example.com"
const PUSH_IMAGE_URL = "https://avatars.githubusercontent.com/u/208285284?v=4&s=216";

// Keep messages compact enough for common push channels.
const MAX_TITLE_LENGTH = 80;
const MAX_MESSAGE_LENGTH = 3800;
const MAX_CLIENTS_SHOWN = 5;

// ==================== Region Helpers ====================
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
  "新西兰": "NZ", "new zealand": "NZ",
  "印度": "IN", "孟买": "IN", "德里": "IN", "india": "IN", "mumbai": "IN", "delhi": "IN",
  "泰国": "TH", "曼谷": "TH", "thailand": "TH", "bangkok": "TH",
  "越南": "VN", "河内": "VN", "胡志明": "VN", "vietnam": "VN", "hanoi": "VN", "ho chi minh": "VN",
  "马来西亚": "MY", "吉隆坡": "MY", "malaysia": "MY", "kuala lumpur": "MY",
  "菲律宾": "PH", "马尼拉": "PH", "philippines": "PH", "manila": "PH",
  "印度尼西亚": "ID", "印尼": "ID", "雅加达": "ID", "indonesia": "ID", "jakarta": "ID",
  "土耳其": "TR", "伊斯坦布尔": "TR", "turkey": "TR", "istanbul": "TR",
  "阿联酋": "AE", "迪拜": "AE", "united arab emirates": "AE", "uae": "AE", "dubai": "AE",
  "沙特": "SA", "沙特阿拉伯": "SA", "saudi arabia": "SA",
  "巴西": "BR", "圣保罗": "BR", "brazil": "BR", "sao paulo": "BR",
  "墨西哥": "MX", "mexico": "MX", "阿根廷": "AR", "argentina": "AR", "智利": "CL", "chile": "CL",
  "南非": "ZA", "south africa": "ZA", "埃及": "EG", "egypt": "EG", "以色列": "IL", "israel": "IL",
  "意大利": "IT", "米兰": "IT", "罗马": "IT", "italy": "IT", "milan": "IT", "rome": "IT",
  "西班牙": "ES", "马德里": "ES", "spain": "ES", "madrid": "ES",
  "葡萄牙": "PT", "portugal": "PT", "瑞士": "CH", "苏黎世": "CH", "switzerland": "CH", "zurich": "CH",
  "瑞典": "SE", "sweden": "SE", "挪威": "NO", "norway": "NO", "芬兰": "FI", "finland": "FI",
  "丹麦": "DK", "denmark": "DK", "波兰": "PL", "poland": "PL", "奥地利": "AT", "austria": "AT",
  "捷克": "CZ", "czech": "CZ", "爱尔兰": "IE", "ireland": "IE", "冰岛": "IS", "iceland": "IS"
};

// ==================== Format Helpers ====================
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

function limitText(text, maxLength) {
  text = asString(text, "");
  if (text.length <= maxLength) return text;
  return text.slice(0, Math.max(0, maxLength - 16)) + "\n...(已截断)";
}

function normalizePanelUrl() {
  return asString(PANEL_URL, "").replace(/\/+$/, "");
}

function getInstanceUrl(uuid) {
  const base = normalizePanelUrl();
  if (!base || !uuid) return base;
  return base + "/instance/" + encodeURIComponent(uuid);
}

function formatCSTTime(timeValue) {
  let date;
  if (!timeValue || String(timeValue).indexOf("0001") === 0) {
    date = new Date();
  } else {
    date = new Date(String(timeValue).replace(/\.\d+Z$/, "Z"));
  }
  if (isNaN(date.getTime())) date = new Date();

  const cst = new Date(date.getTime() + 8 * 60 * 60 * 1000);
  const pad = function (n) { return String(n).padStart(2, "0"); };
  return cst.getUTCFullYear() + "-" + pad(cst.getUTCMonth() + 1) + "-" + pad(cst.getUTCDate()) +
    " " + pad(cst.getUTCHours()) + ":" + pad(cst.getUTCMinutes()) + ":" + pad(cst.getUTCSeconds());
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

function hideIP(ip) {
  ip = asString(ip, "").trim();
  if (!ip) return "未知";
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

function hasFlagEmoji(text) {
  return /[\uD83C][\uDDE6-\uDDFF][\uD83C][\uDDE6-\uDDFF]/.test(asString(text, ""));
}

function countryCodeToFlag(code) {
  code = asString(code, "").toUpperCase().trim();
  if (!code || code.length !== 2 || !VALID_COUNTRY_CODES[code]) return "";
  try {
    return String.fromCodePoint(127397 + code.charCodeAt(0)) +
      String.fromCodePoint(127397 + code.charCodeAt(1));
  } catch (e) {
    return "";
  }
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

function parseBillingDesc(price, currency, cycleValue) {
  if (price === undefined || price === null || price === -1 || String(price).toLowerCase() === "free") {
    return "免费";
  }
  const symbol = currency || "$";
  let cycle = asString(cycleValue, "").toLowerCase().trim();
  if (!cycle || cycle === "0") {
    cycle = "";
  } else if (!isNaN(Number(cycle))) {
    cycle = "/" + cycle + "天";
  } else {
    const alias = { month: "/月", monthly: "/月", year: "/年", yearly: "/年", quarter: "/季", weekly: "/周", week: "/周" };
    cycle = alias[cycle] || "/" + cycleValue;
  }
  return symbol + price + cycle;
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
    [/\berror\b/gi, "错误"], [/\bfailed\b/gi, "失败"], [/\bsuccess\b/gi, "成功"],
    [/\btimeout\b/gi, "超时"], [/\bexpire(d)?\b/gi, "到期"], [/\brenew\b/gi, "续费"],
    [/\btest\b/gi, "测试"], [/\busage\b/gi, "使用率"], [/\bhigh\b/gi, "过高"]
  ];
  for (let i = 0; i < rules.length; i++) {
    msg = msg.replace(rules[i][0], rules[i][1]);
  }
  return msg;
}

// ==================== Event Formatting ====================
const EVENT_MAP = {
  online: { icon: "🟢", title: "服务器上线", level: "正常" },
  offline: { icon: "🔴", title: "服务器离线", level: "异常" },
  alert: { icon: "⚠️", title: "监控告警", level: "警告" },
  traffic: { icon: "📶", title: "流量告警", level: "警告" },
  renew: { icon: "💰", title: "续费通知", level: "提醒" },
  expire: { icon: "🚨", title: "到期预警", level: "重要" },
  expired: { icon: "🚨", title: "服务到期", level: "重要" },
  login: { icon: "🔐", title: "登录通知", level: "提醒" },
  test: { icon: "🧪", title: "测试通知", level: "测试" },
  recover: { icon: "🟢", title: "告警恢复", level: "正常" },
  recovered: { icon: "🟢", title: "告警恢复", level: "正常" },
  report: { icon: "📊", title: "流量定时报告", level: "报告" }
};

function normalizeEventName(event) {
  let name = asString(event && event.event, "").toLowerCase().trim();
  const message = asString(event && event.message, "");
  if (name.indexOf("report") >= 0 || message.indexOf("流量报告") >= 0 || message.toLowerCase().indexOf("traffic report") >= 0) {
    return "report";
  }
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

function trafficTypeLabel(type) {
  type = asString(type, "max").toLowerCase().trim();
  const map = {
    sum: "总和", max: "取最大", min: "取最小", up: "仅上传", down: "仅下载"
  };
  return map[type] || type;
}

function formatClientCard(client, index, compact) {
  client = client || {};
  const uuid = firstNonEmpty([client.uuid, client.UUID], "");
  const flag = getFlag(client);
  const region = asString(client.region, "");
  const name = firstNonEmpty([client.name, uuid], "未知节点");
  const provider = asString(client.provider, "");
  const role = asString(client.business_role, "");
  const group = asString(client.group, "");

  let lines = [];
  const prefix = index !== undefined && index !== null ? (index + 1) + ". " : "";
  lines.push(prefix + "🖥️ " + (flag ? flag + " " : "") + name + (region ? " [" + region + "]" : ""));

  const meta = [];
  if (provider) meta.push(provider);
  if (role) meta.push(role);
  if (group) meta.push(group);
  if (meta.length > 0) lines.push("   🏷️ " + meta.join(" / "));

  if (!compact) {
    const cpuCores = firstNonEmpty([client.cpu_cores, client.cpuCores], 0);
    const mem = formatMemory(firstNonEmpty([client.mem_total, client.memTotal], 0));
    const swap = formatMemory(firstNonEmpty([client.swap_total, client.swapTotal], 0));
    const disk = formatMemory(firstNonEmpty([client.disk_total, client.diskTotal], 0));
    lines.push("   ⚙️ " + cpuCores + "C / " + mem + (swap !== "0" ? "+" + swap : "") + " / " + disk);

    const osParts = [client.os, client.arch, client.version].filter(Boolean);
    if (osParts.length > 0) lines.push("   🧩 " + osParts.join(" / "));

    const ipv4 = hideIP(firstNonEmpty([client.ipv4, client.IPv4], ""));
    const ipv6 = hideIP(firstNonEmpty([client.ipv6, client.IPv6], ""));
    if (ipv4 !== "未知") lines.push("   🌐 IPv4: " + ipv4);
    if (ipv6 !== "未知") lines.push("   🌐 IPv6: " + ipv6);

    const trafficLimit = firstNonEmpty([client.traffic_limit, client.trafficLimit], 0);
    const trafficType = firstNonEmpty([client.traffic_limit_type, client.trafficLimitType], "max");
    lines.push("   📶 流量: " + formatTrafficLimit(trafficLimit) + " (" + trafficTypeLabel(trafficType) + ")");

    const billing = parseBillingDesc(client.price, client.currency, firstNonEmpty([client.billing_cycle, client.billingCycle], ""));
    lines.push("   💰 账单: " + billing);

    const expiredAt = firstNonEmpty([client.expired_at, client.expiredAt], "");
    if (expiredAt) lines.push("   ⏳ 到期: " + formatCSTTime(expiredAt));

    const url = getInstanceUrl(uuid);
    if (url) lines.push("   🔗 " + url);
  }

  return lines.join("\n");
}

function normalizeClients(event) {
  if (!event) return [];
  if (Array.isArray(event.clients)) return event.clients;
  if (Array.isArray(event.Clients)) return event.Clients;
  if (event.client) return [event.client];
  if (event.Client) return [event.Client];
  return [];
}

function formatClientsSection(clients) {
  if (!clients || clients.length === 0) return "🖥️ 服务器: 全局系统级事件";
  if (clients.length === 1) return formatClientCard(clients[0], null, false);

  const lines = ["📦 关联节点: " + clients.length + " 台"];
  const shown = Math.min(clients.length, MAX_CLIENTS_SHOWN);
  for (let i = 0; i < shown; i++) {
    lines.push(formatClientCard(clients[i], i, true));
  }
  if (clients.length > shown) {
    lines.push("📌 仅展示前 " + shown + " 台，其余 " + (clients.length - shown) + " 台请前往面板查看。");
  }
  return lines.join("\n");
}

// ==================== MeoW Sender ====================
async function sendMessage(message, title, instanceId) {
  if (!MEOW_NICKNAME) {
    console.error("MEOW_NICKNAME is empty.");
    return false;
  }

  const apiUrl = "https://api.chuckfang.com/" + encodeURIComponent(MEOW_NICKNAME);
  const jumpUrl = instanceId ? getInstanceUrl(instanceId) : normalizePanelUrl();

  try {
    const resp = await fetch(apiUrl, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        title: limitText(title || "Komari 通知", MAX_TITLE_LENGTH),
        msg: limitText(message || "", MAX_MESSAGE_LENGTH),
        url: jumpUrl || "",
        imgUrl: PUSH_IMAGE_URL
      })
    });

    let result = {};
    try {
      result = await resp.json();
    } catch (e) {
      const text = await resp.text();
      result = { raw: text };
    }

    if (!resp.ok) {
      console.error("MeoW push HTTP failed:", resp.status, resp.statusText, JSON.stringify(result));
      return false;
    }
    if (result && result.status !== undefined && Number(result.status) !== 200) {
      console.error("MeoW push API failed:", JSON.stringify(result));
      return false;
    }
    return true;
  } catch (error) {
    console.error("MeoW push error:", error);
    return false;
  }
}

async function sendEvent(event) {
  event = event || {};
  try {
    const eventName = normalizeEventName(event);
    const info = EVENT_MAP[eventName] || { icon: event.emoji || "📌", title: "系统通知", level: "通知" };
    const clients = normalizeClients(event);
    const targetInstanceId = clients.length === 1 ? firstNonEmpty([clients[0].uuid, clients[0].UUID], null) : null;

    const title = info.icon + " " + info.title;
    const lines = [];
    lines.push(formatClientsSection(clients));
    lines.push("");
    lines.push("📊 事件级别: " + info.level);
    lines.push("🕒 北京时间: " + formatCSTTime(event.time || event.Time));

    const rawMessage = firstNonEmpty([event.message, event.Message], "");
    const translated = translateMessage(rawMessage);
    if (translated) {
      lines.push("");
      lines.push("📄 详细描述:");
      lines.push(translated);
    }

    return await sendMessage(lines.join("\n"), title, targetInstanceId);
  } catch (error) {
    console.error("sendEvent failed:", error);
    const fallbackTitle = "⚠️ Komari 通知脚本异常";
    const fallbackMessage =
      "事件: " + asString(event && event.event, "未知") + "\n" +
      "时间: " + formatCSTTime(event && (event.time || event.Time)) + "\n" +
      "错误: " + asString(error && error.message, error);
    try {
      return await sendMessage(fallbackMessage, fallbackTitle);
    } catch (fallbackError) {
      console.error("fallback notification failed:", fallbackError);
      return false;
    }
  }
}
