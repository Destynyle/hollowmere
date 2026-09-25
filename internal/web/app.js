"use strict";
// TAP GUI client. All server data is rendered with textContent (never HTML).

const $ = (sel) => document.querySelector(sel);
const $$ = (sel) => Array.from(document.querySelectorAll(sel));

const state = {
  ws: null,
  wantConnection: false,
  retryDelay: 1000,
  retryTimer: null,
  connected: false,
  greeted: false,
  pending: [],          // [{cmd, line, cb}] awaiting OK/ERR, in order
  names: {},            // id -> display name cache
  inventory: [],
  room: null,
  me: "",
  scope: "GLOBAL",
  unread: { GLOBAL: 0, ROOM: 0, GROUP: 0 },
  inCombat: false,
  refreshTimer: null,
  asked: {},            // ids already sent to INSPECT
};

// ---------------------------------------------------------------- helpers
function el(tag, cls, text) {
  const e = document.createElement(tag);
  if (cls) e.className = cls;
  if (text !== undefined) e.textContent = text;
  return e;
}
function button(label, onClick, cls = "mini") {
  const b = el("button", cls, label);
  b.type = "button";
  b.addEventListener("click", onClick);
  return b;
}
function now() {
  return new Date().toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit" });
}
function tryJSON(s) {
  try { return JSON.parse(s); } catch { return undefined; }
}
function prettyId(id) {
  // "item.healing_potion#2" -> "healing potion #2"
  const [base, n] = String(id).split("#");
  const short = base.includes(".") ? base.slice(base.indexOf(".") + 1) : base;
  return short.replace(/_/g, " ") + (n ? " #" + n : "");
}
function nameOf(id) {
  return state.names[id] || prettyId(id);
}
function appendFeed(feed, parts, cls) {
  const li = el("li", cls);
  li.appendChild(el("span", "time", now()));
  for (const p of parts) li.appendChild(typeof p === "string" ? document.createTextNode(p) : p);
  const stick = feed.scrollTop + feed.clientHeight >= feed.scrollHeight - 8;
  feed.appendChild(li);
  while (feed.children.length > 500) feed.removeChild(feed.firstChild);
  if (stick) feed.scrollTop = feed.scrollHeight;
}
function log(text, cls = "") {
  appendFeed($("#log"), [text], cls);
}

// ---------------------------------------------------------------- transport
// One WebSocket carries the protocol: one text message per line, in order.
function wsURL() {
  const scheme = location.protocol === "https:" ? "wss" : "ws";
  return `${scheme}://${location.host}/ws`;
}

// quiet: background refresh, errors are not shown (another group's server
// may not know our extension commands).
function send(line, cb, quiet = false) {
  if (!state.ws || state.ws.readyState !== WebSocket.OPEN) {
    log("Not connected.", "err");
    return;
  }
  const cmd = line.trim().split(/\s+/)[0].toUpperCase();
  state.pending.push({ cmd, line, cb, quiet });
  if ($("#log-raw").checked) log("> " + line, "out");
  state.ws.send(line);
}

// The resume key is this character's only secret: no account, no password.
function loadKey() {
  try { return localStorage.getItem("tap-key") || ""; } catch { return ""; }
}
function storeKey(key) {
  try { localStorage.setItem("tap-key", key); } catch { /* private window */ }
  const field = $("#resume-key");
  field.value = key;
  field.placeholder = "";
}

function connect(name) {
  disconnectLocal();
  state.me = name;
  state.wantConnection = true;
  setConn("connecting…", false);
  let ws;
  try {
    ws = new WebSocket(wsURL());
  } catch (e) {
    setConn("offline", false);
    log("Cannot open a connection: " + e.message, "err");
    return;
  }
  state.ws = ws;
  state.greeted = false;
  state.pending = [];

  ws.onopen = () => {
    state.retryDelay = 1000;
    // The greeting arrives first; CONNECT can be queued right away.
    const key = loadKey();
    send("CONNECT " + name + (key ? " " + key : ""), (ok, payload) => {
      if (!ok) {
        log("Cannot join: " + payload, "err");
        state.wantConnection = false;   // a bad name is not worth retrying
        return;
      }
      setConn("online as " + name, true);
      refreshAll();
    });
  };
  ws.onmessage = (m) => onLine(m.data);
  ws.onclose = (ev) => {
    if (state.ws !== ws) return;
    state.ws = null;
    setConn("offline", false);
    if (!state.wantConnection) return;
    log(`Connection lost${ev.reason ? " (" + ev.reason + ")" : ""}. Reconnecting…`, "err");
    scheduleReconnect(name);
  };
  ws.onerror = () => { /* onclose always follows */ };
}

// Reconnect with a growing delay, so a server restart does not need a
// page reload.
function scheduleReconnect(name) {
  clearTimeout(state.retryTimer);
  const delay = state.retryDelay;
  state.retryDelay = Math.min(delay * 2, 30000);
  setConn(`reconnecting in ${Math.round(delay / 1000)}s`, false);
  state.retryTimer = setTimeout(() => {
    if (state.wantConnection) connect(name);
  }, delay);
}

function disconnectLocal() {
  clearTimeout(state.retryTimer);
  if (state.ws) {
    const ws = state.ws;
    state.ws = null;
    try { ws.close(1000, "bye"); } catch { /* ignore */ }
  }
  state.greeted = false;
  state.pending = [];
  setConn("offline", false);
}

function setConn(text, on) {
  state.connected = on;
  const s = $("#conn-state");
  s.textContent = text;
  s.className = "state " + (on ? "on" : "off");
  $$("main button:not(.tab)").forEach((b) => { b.disabled = !on; });
}

// ---------------------------------------------------------------- incoming
function onLine(line) {
  const sp = line.indexOf(" ");
  const kind = sp < 0 ? line : line.slice(0, sp);
  const payload = sp < 0 ? "" : line.slice(sp + 1);
  if ($("#log-raw").checked) log("< " + line, kind === "ERR" ? "err" : "evt");

  if (kind === "EVT") return onEvent(payload);
  if (kind !== "OK" && kind !== "ERR") return log(line);
  if (!state.greeted && kind === "OK" && payload.startsWith("hello")) {
    state.greeted = true;
    log("Server greeting: " + payload, "ok");
    return;
  }
  const p = state.pending.shift();
  const ok = kind === "OK";
  if (!ok && !(p && p.quiet)) log(`${p ? p.cmd + ": " : ""}ERR ${payload}`, "err");
  if (p) {
    handleReply(p.cmd, ok, payload, p.line);
    if (p.cb) p.cb(ok, payload);
  }
}

function onEvent(payload) {
  const parts = payload.split(" ");
  const [scope, type] = parts;
  if (type === "CHAT") {
    const user = parts[2] || "?";
    const msg = parts.slice(3).join(" ");
    addChat(scope, user, msg);
    return;
  }
  switch (scope) {
    case "ROOM":
      if (type === "PRESENCE") {
        log(`${parts[3]} ${parts[2] === "ENTER" ? "entered" : "left"} the room.`, "evt");
        scheduleRefresh(false);
      } else if (type === "COMBAT") {
        appendFeed($("#log"), ["⚔ " + parts.slice(2).join(" ")], "combat");
        scheduleRefresh(false);
      } else {
        // ITEM / NPC extension events: someone changed the room.
        log("Room: " + parts.slice(1).join(" "), "evt");
        scheduleRefresh(false);
      }
      break;
    case "GROUP":
      if (type === "INVITE") {
        const leader = parts[2];
        const li = el("li");
        li.appendChild(el("span", "time", now()));
        li.appendChild(document.createTextNode(`✉ ${leader} invites you to a group `));
        li.appendChild(button("Join", () => send("GROUP JOIN " + leader)));
        const feed = $('[data-feed="GROUP"]');
        feed.appendChild(li);
        bump("GROUP");
        log(`Group invitation from ${leader}.`, "quest");
      } else {
        addChat("GROUP", "*", `${parts[2]} ${type === "JOIN" ? "joined" : "left"} the group`);
        refresh("GROUP INFO");
      }
      break;
    case "STATS": {
      const m = /players=(\d+)/.exec(payload);
      if (m) $("#count-server").textContent = m[1];
      break;
    }
    case "QUEST":
      log(`★ Quest ${parts[1].toLowerCase()}: ${parts.slice(2).join(" ")}`, "quest");
      refresh("QUESTS");
      break;
    case "PLAYER":
      if (type === "KEY") {
        storeKey(parts[2]);
        log("Character saved. Your resume key brings it back on any device.", "ok");
      } else if (type === "AMBUSH") {
        appendFeed($("#log"), [`⚔ ${nameOf(parts[2])} ambushes you for ${parts[3]} damage!`], "combat");
        scheduleRefresh(true);
      } else if (type === "RESPAWN") {
        log("☠ You were defeated and respawn in " + parts[2], "err");
        scheduleRefresh(true);
      }
      break;
    case "SERVER":
      log("📢 " + payload.slice(7), "quest");
      break;
    default:
      log("Event: " + payload, "evt");
  }
}

function addChat(scope, user, msg) {
  if (!(scope in state.unread)) return log(`[${scope}] ${user}: ${msg}`);
  const feed = $(`[data-feed="${scope}"]`);
  appendFeed(feed, [el("span", "who", user + ": "), msg]);
  bump(scope);
}
function bump(scope) {
  if (scope === state.scope) return;
  state.unread[scope]++;
  $(`[data-badge="${scope}"]`).textContent = String(state.unread[scope]);
}

// ---------------------------------------------------------------- replies
function handleReply(cmd, ok, payload, line) {
  switch (cmd) {
    case "LOOK": if (ok) renderLook(payload); break;
    case "INVENTORY": if (ok) renderInventory(payload); break;
    case "STATUS": if (ok) renderStatus(payload); break;
    case "QUESTS": if (ok) renderQuests(payload); break;
    case "WHO": if (ok) renderWho(payload); break;
    case "TALK": if (ok) renderTalk(payload, line); break;
    case "INSPECT":
      if (ok) {
        const d = tryJSON(payload);
        if (d && d.id) {
          state.names[d.id] = d.name;
          log(`${d.name} (${d.id}): ${d.description}`, "ok");
          renderInventoryList();
        }
      }
      break;
    case "MOVE":
      if (ok) { log("You go to " + payload.replace("room=", ""), "ok"); hideDialogue(); refresh("LOOK", "WHO"); }
      break;
    case "TAKE":
    case "DROP":
      if (ok) { log(payload.replace("=", ": "), "ok"); refresh("LOOK", "INVENTORY", "STATUS", "QUESTS"); }
      break;
    case "ATTACK":
    case "DEFEND":
    case "FLEE":
    case "USE":
      if (ok) renderCombat(payload);
      refresh("LOOK", "STATUS", ...(cmd === "ATTACK" && !ok ? [] : ["INVENTORY"]));
      break;
    case "QUEST":
      if (ok) {
        const q = tryJSON(payload);
        if (q && q.quest_id) {
          log(`★ ${q.title || q.quest_id} [${q.status}] ${q.progress || ""} — ${q.description || ""}` +
              (q.reward ? ` (reward: ${q.reward})` : "") + (q.message ? " " + q.message : ""), "quest");
        } else log(payload, "quest");
        refresh("INVENTORY", "STATUS", "QUESTS");
      }
      break;
    case "GROUP":
      if (ok && payload.startsWith("group=")) {
        log("Group: " + payload.slice(6), "ok");
        refresh("GROUP INFO");
      } else if (ok && payload.startsWith("{")) {
        renderGroup(payload);
      } else if (ok && line.toUpperCase().includes("LEAVE")) {
        $("#group-info").textContent = "No group.";
      } else if (ok) {
        log("Group: done", "ok");
      } else if (line.toUpperCase().includes("INFO")) {
        $("#group-info").textContent = "No group.";
      }
      break;
    case "QUIT":
      state.wantConnection = false;
      log("Bye!", "ok");
      break;
    case "CHAT":
    case "CONNECT":
      break;
    default:
      if (ok) log(`${cmd}: ${payload || "OK"}`, "ok");
  }
}

function refresh(...cmds) {
  cmds.forEach((c) => send(c, null, true));
}
function refreshAll() {
  refresh("LOOK", "INVENTORY", "STATUS", "QUESTS", "WHO", "GROUP INFO");
}
function scheduleRefresh(all) {
  clearTimeout(state.refreshTimer);
  state.refreshTimer = setTimeout(() => {
    if (!state.connected) return;
    if (all) refreshAll(); else refresh("LOOK", "WHO");
  }, 150);
}

function emptyItem(list, text) {
  list.appendChild(el("li", "empty", text));
}

function renderLook(payload) {
  const d = tryJSON(payload);
  if (!d || !d.room) return log(payload);
  state.room = d;
  const r = d.room;
  $("#room-name").textContent = r.name || r.id;
  $("#room-id").textContent = r.id || "";
  $("#room-desc").textContent = r.description || "";

  const details = d.details || {};
  const itemInfo = {};
  (details.items || []).forEach((i) => { state.names[i.id] = i.name; itemInfo[i.id] = i; });
  const npcInfo = {};
  (details.npcs || []).forEach((n) => { state.names[n.id] = n.name; npcInfo[n.id] = n; });

  const exits = r.exits || {};
  $$("#compass [data-dir]").forEach((b) => {
    b.disabled = !state.connected || !(b.dataset.dir in exits);
    b.title = exits[b.dataset.dir] || "";
  });

  const items = $("#room-items");
  items.replaceChildren();
  (d.items || []).forEach((id) => {
    const li = el("li");
    li.appendChild(el("span", "name", nameOf(id)));
    li.appendChild(el("span", "id", id));
    li.appendChild(button("Take", () => send("TAKE " + id)));
    li.appendChild(button("?", () => send("INSPECT " + id)));
    items.appendChild(li);
  });
  if (!(d.items || []).length) emptyItem(items, "nothing");

  const npcs = $("#room-npcs");
  npcs.replaceChildren();
  (d.npcs || []).forEach((id) => {
    const info = npcInfo[id] || {};
    const li = el("li");
    li.appendChild(el("span", "name", nameOf(id)));
    if (info.hostile) li.appendChild(el("span", "tag hostile", `hostile ${info.hp}/${info.max_hp}`));
    else if (info.role) li.appendChild(el("span", "tag" + (info.role === "quest_giver" ? " quest" : ""), info.role.replace("_", " ")));
    li.appendChild(button("Talk", () => send("TALK " + id)));
    li.appendChild(button("Quest", () => send("QUEST " + id)));
    li.appendChild(button("Attack", () => send("ATTACK " + id)));
    npcs.appendChild(li);
  });
  if (!(d.npcs || []).length) emptyItem(npcs, "nobody");

  const players = $("#room-players");
  players.replaceChildren();
  (d.players || []).forEach((p) => {
    const li = el("li");
    li.appendChild(el("span", "name", p === state.me ? p + " (you)" : p));
    if (p !== state.me) li.appendChild(button("Invite", () => send("GROUP INVITE " + p)));
    players.appendChild(li);
  });
  $("#count-room").textContent = String((d.players || []).length);
  // Ask for names we do not know yet (inventory items seen elsewhere).
  renderInventoryList();
}

function renderInventory(payload) {
  const inv = tryJSON(payload);
  if (!Array.isArray(inv)) return log(payload);
  state.inventory = inv;
  inv.forEach((id) => {
    if (!state.names[id] && !state.asked[id]) {
      state.asked[id] = true;
      send("INSPECT " + id, null, true);
    }
  });
  renderInventoryList();
}

function renderInventoryList() {
  const list = $("#inventory");
  list.replaceChildren();
  state.inventory.forEach((id) => {
    const li = el("li");
    li.appendChild(el("span", "name", nameOf(id)));
    li.appendChild(el("span", "id", id));
    li.appendChild(button("Drop", () => send("DROP " + id)));
    li.appendChild(button("Use", () => send("USE " + id)));
    list.appendChild(li);
  });
  if (!state.inventory.length) emptyItem(list, "empty");
}

function setBar(fill, hp, max) {
  const pct = max > 0 ? Math.max(0, Math.min(100, (hp / max) * 100)) : 0;
  fill.style.width = pct + "%";
  if (fill.id !== "enemy-fill") {
    fill.style.backgroundColor = pct > 60 ? "var(--accent)" : pct > 30 ? "var(--accent-2)" : "var(--danger)";
  }
}

// Ten pixel hearts, Minecraft style: each heart is 10% of max HP, halves included.
function renderHearts(hp, max) {
  const box = $("#hearts");
  box.replaceChildren();
  const halves = max > 0 ? Math.ceil((Math.max(0, hp) / max) * 20) : 0;
  for (let i = 0; i < 10; i++) {
    const left = halves - i * 2;
    const kind = left >= 2 ? "full" : left === 1 ? "half" : "empty";
    box.appendChild(el("span", "heart " + kind));
  }
  box.classList.toggle("low", max > 0 && hp / max <= 0.3);
}

function renderStatus(payload) {
  const s = tryJSON(payload);
  if (!s) return log(payload);
  $("#hp-text").textContent = `${s.hp}/${s.max_hp} ${s.status || ""}`;
  setBar($("#hp-fill"), s.hp, s.max_hp);
  renderHearts(s.hp, s.max_hp);
  const fighting = s.status === "combat" || s.in_combat;
  state.inCombat = !!fighting;
  $("#combat-box").hidden = !fighting;
  if (fighting && s.target) {
    const thp = typeof s.target_hp === "number" ? s.target_hp : 0;
    $("#combat-target").textContent = `${nameOf(s.target)} (${thp} HP)`;
    const npcs = (state.room && state.room.details && state.room.details.npcs) || [];
    const info = npcs.find((n) => n.id === s.target);
    setBar($("#enemy-fill"), thp, (info && info.max_hp) || thp || 1);
    $("#btn-attack-again").onclick = () => send("ATTACK " + s.target);
  }
}

function renderCombat(payload) {
  const r = tryJSON(payload);
  if (!r) return log(payload, "combat");
  (r.log || []).forEach((l) => appendFeed($("#log"), ["⚔ " + l], "combat"));
  if (r.used) log(`Used ${nameOf(r.used)}: +${r.healed} HP`, "ok");
  if (r.status === "victory") log("Victory!", "ok");
  if (r.status === "fled") { log(`You fled ${r.direction}.`, "ok"); hideDialogue(); }
  if (r.status === "defeated") log(`Defeated! Respawned at ${r.respawn_room}.`, "err");
  if (!r.log && !r.used && !r.status) log(payload, "combat");
}

function renderQuests(payload) {
  const qs = tryJSON(payload);
  const list = $("#quests");
  list.replaceChildren();
  if (!Array.isArray(qs)) return log(payload);
  qs.forEach((q) => {
    const li = el("li", q.status);
    li.appendChild(el("span", "name", q.title || q.quest_id));
    if (q.progress) li.appendChild(el("span", "progress", q.progress));
    li.appendChild(el("span", "tag", q.status));
    if (q.status === "active") li.appendChild(button("Abandon", () => send("ABANDON " + q.quest_id)));
    li.title = q.description || "";
    list.appendChild(li);
  });
  if (!qs.length) emptyItem(list, "no quests yet — talk to villagers");
}

function renderWho(payload) {
  const w = tryJSON(payload);
  if (w && typeof w === "object") {
    if (Array.isArray(w.room)) $("#count-room").textContent = String(w.room.length);
    if (typeof w.server === "number") $("#count-server").textContent = String(w.server);
    return;
  }
  const m = /players=(\d+)/.exec(payload); // RFC 5.2.2 form
  if (m) $("#count-server").textContent = m[1];
}

function renderTalk(payload, line) {
  const t = tryJSON(payload);
  let speaker = line.split(/\s+/).slice(1).join(" ");
  let text = payload;
  if (t && typeof t === "object") {
    speaker = t.name || nameOf(t.npc) || speaker;
    text = t.dialogue;
  } else {
    speaker = nameOf(speaker);
  }
  $("#dialogue").hidden = false;
  $("#dialogue-speaker").textContent = speaker;
  $("#dialogue-line").textContent = "“" + text + "”";
  log(`${speaker}: ${text}`, "quest");
}
function hideDialogue() { $("#dialogue").hidden = true; }

function renderGroup(payload) {
  const g = tryJSON(payload);
  if (!g) return;
  $("#group-info").textContent = `${g.group} — leader ${g.leader} — members: ${(g.members || []).join(", ")}`;
}

// ---------------------------------------------------------------- wiring
function valueOf(sel) {
  const v = $(sel).value.trim();
  if (!v) log("Please fill in the field first.", "err");
  return v;
}

function init() {
  // Optional URL parameters: ?name=alice&autoconnect=1
  const params = new URLSearchParams(location.search);
  try { $("#name").value = localStorage.getItem("tap-name") || ""; } catch { /* ignore */ }
  if (params.get("name")) $("#name").value = params.get("name");
  setConn("offline", false);
  if (params.get("autoconnect") && $("#name").value.trim()) {
    connect($("#name").value.trim());
  }

  $("#connect-form").addEventListener("submit", (e) => {
    e.preventDefault();
    const name = $("#name").value.trim();
    try { localStorage.setItem("tap-name", name); } catch { /* ignore */ }
    connect(name);
  });

  $$("#compass [data-dir]").forEach((b) => b.addEventListener("click", () => send("MOVE " + b.dataset.dir)));
  $("#btn-look").addEventListener("click", () => send("LOOK"));
  $$("[data-cmd]").forEach((b) => b.addEventListener("click", () => send(b.dataset.cmd)));
  $$("[data-target]").forEach((b) => b.addEventListener("click", () => {
    const v = valueOf("#target-name");
    if (v) send(b.dataset.target + " " + v);
  }));
  $("#btn-quit").addEventListener("click", () => send("QUIT"));

  $("#btn-take-name").addEventListener("click", () => { const v = valueOf("#item-name"); if (v) send("TAKE " + v); });
  $("#btn-drop-name").addEventListener("click", () => { const v = valueOf("#item-name"); if (v) send("DROP " + v); });
  $("#btn-use-name").addEventListener("click", () => { const v = valueOf("#item-name"); if (v) send("USE " + v); });
  $("#btn-defend").addEventListener("click", () => send("DEFEND"));
  $("#btn-flee").addEventListener("click", () => send("FLEE"));

  $("#resume-key").value = loadKey();
  $("#btn-show-key").addEventListener("click", () => {
    const f = $("#resume-key");
    f.type = f.type === "password" ? "text" : "password";
  });
  $("#btn-copy-key").addEventListener("click", async () => {
    const key = $("#resume-key").value;
    if (!key) { log("No key yet — join the world first.", "err"); return; }
    try {
      await navigator.clipboard.writeText(key);
      log("Resume key copied to the clipboard.", "ok");
    } catch {
      $("#resume-key").type = "text";
      $("#resume-key").select();
      log("Copy it by hand: the browser refused clipboard access.", "err");
    }
  });
  $("#btn-new-char").addEventListener("click", () => {
    if (!confirm("Forget this key and start a new character? The old one is lost unless you copied its key.")) return;
    try { localStorage.removeItem("tap-key"); } catch { /* ignore */ }
    $("#resume-key").value = "";
    $("#resume-key").placeholder = "a new key will arrive when you join";
    disconnectLocal();
    state.wantConnection = false;
    log("Key forgotten. Pick a name and join to create a new character.", "ok");
  });

  $("#btn-group-create").addEventListener("click", () => send("GROUP CREATE"));
  $("#btn-group-invite").addEventListener("click", () => { const v = valueOf("#group-player"); if (v) send("GROUP INVITE " + v); });
  $("#btn-group-join").addEventListener("click", () => { const v = valueOf("#group-player"); if (v) send("GROUP JOIN " + v); });
  $("#btn-group-leave").addEventListener("click", () => send("GROUP LEAVE"));

  $$(".tab").forEach((t) => t.addEventListener("click", () => {
    state.scope = t.dataset.scope;
    state.unread[state.scope] = 0;
    $(`[data-badge="${state.scope}"]`).textContent = "";
    $$(".tab").forEach((x) => x.classList.toggle("active", x === t));
    $$("[data-feed]").forEach((f) => { f.hidden = f.dataset.feed !== state.scope; });
    const label = { GLOBAL: "Global", ROOM: "the room", GROUP: "your group" }[state.scope];
    $("#chat-input").placeholder = `Say something to ${label}…`;
  }));

  $("#chat-form").addEventListener("submit", (e) => {
    e.preventDefault();
    const msg = $("#chat-input").value.trim();
    if (!msg) return;
    send(`CHAT ${state.scope} ${msg}`);
    $("#chat-input").value = "";
  });
  $("#raw-form").addEventListener("submit", (e) => {
    e.preventDefault();
    const line = $("#raw-input").value.trim();
    if (!line) return;
    if (!$("#log-raw").checked) log("> " + line, "out");
    send(line);
    $("#raw-input").value = "";
  });

  window.addEventListener("beforeunload", () => {
    state.wantConnection = false;
    if (state.ws) try { state.ws.close(1000, "bye"); } catch { /* ignore */ }
  });
}

init();
