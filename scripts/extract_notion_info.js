// extract_notion_info.js — helper for adding a Notion account to notiongate.
//
// Usage:
//   1. Open https://www.notion.so in Chrome/Firefox and log in.
//   2. Press F12 → Console tab.
//   3. Paste this whole file and press Enter.
//   4. Copy token_v2: DevTools → Application → Cookies → https://www.notion.so → token_v2 → Value.
//   5. Paste the printed JSON into `notiongate accounts add` / POST /admin/accounts,
//      or just run:
//        notiongate accounts add --cookie "<token_v2 value>"
//
// The script only reads data from your own logged-in session (same-origin
// requests to notion.so). Nothing is sent anywhere else.
(async () => {
  const post = async (ep, body = {}) => {
    const r = await fetch(`https://www.notion.so/api/v3/${ep}`, {
      method: "POST",
      credentials: "include",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
    });
    if (!r.ok) throw new Error(`${ep}: HTTP ${r.status}`);
    return r.json();
  };

  const out = { token_v2: "<вставьте сюда значение cookie token_v2>" };

  try {
    const spaces = await post("getSpaces");
    for (const rec of Array.isArray(spaces) ? spaces : []) {
      const v = rec && rec.value;
      if (!v || !v.id) continue;
      out.space_id = v.id;
      out.space_name = v.name || "";
      if (Array.isArray(v.permissions)) {
        const p = v.permissions.find((x) => x && x.user_id);
        if (p) out.user_id = p.user_id;
      }
      break;
    }
  } catch (e) {
    console.warn("getSpaces failed:", e.message);
  }

  try {
    const luc = await post("loadUserContent");
    const rm = luc && luc.recordMap;
    if (rm && rm.notion_user) {
      for (const [id, v] of Object.entries(rm.notion_user)) {
        out.user_id = out.user_id || id;
        const val = (v && v.value) || v || {};
        out.email = val.email || out.email;
        break;
      }
    }
  } catch (e) {
    console.warn("loadUserContent failed:", e.message);
  }

  try {
    const models = await post("getAvailableModels", { spaceId: out.space_id });
    const collect = (v, acc = []) => {
      if (Array.isArray(v)) {
        for (const e of v) {
          if (typeof e === "string") acc.push(e);
          else if (e && typeof e === "object" && e.id) acc.push(e.id);
          else collect(e, acc);
        }
      } else if (v && typeof v === "object") {
        for (const e of Object.values(v)) collect(e, acc);
      }
      return acc;
    };
    const ids = [...new Set(collect(models))];
    if (ids.length) out.models = ids;
  } catch (e) {
    console.warn("getAvailableModels failed:", e.message);
  }

  console.log(
    "%cСкопируйте блок ниже и используйте с notiongate:\n" +
      "notiongate accounts add --cookie \"<token_v2>\"\n" +
      "(или отправьте этот JSON в POST /admin/accounts)",
    "color:#0a0;font-weight:bold"
  );
  console.log(JSON.stringify(out, null, 2));
  console.log(
    "%ctoken_v2: DevTools → Application → Cookies → https://www.notion.so → token_v2 → Value",
    "color:#f80"
  );
})();
