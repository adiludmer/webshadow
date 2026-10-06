// Webshadow interaction recorder. Runs in an isolated world in every frame
// and reports clicks, typing, changes and form submissions through the
// __webshadowEvent binding. It records what was interacted with, never
// the value of a password, hidden or credential-like field.
(() => {
  const send = globalThis.__webshadowEvent;
  if (typeof send !== "function" || globalThis.__webshadowInstalled) return;
  globalThis.__webshadowInstalled = true;

  const MAX_TEXT = 80;
  const MAX_VALUE = 200;
  const SECRET_AUTOCOMPLETE = /(password|one-time-code|cc-|csc|card|iban|ssn)/i;
  const SECRET_NAME = /(pass|pwd|secret|token|otp|cvv|cvc|card.?num|ssn|\bpin\b)/i;

  const clip = (s, n) => {
    if (s == null) return undefined;
    s = String(s).replace(/\s+/g, " ").trim();
    if (!s) return undefined;
    return s.length > n ? s.slice(0, n) + "…" : s;
  };

  const implicitRole = (el) => {
    const tag = el.tagName.toLowerCase();
    if (tag === "a" && el.hasAttribute("href")) return "link";
    if (tag === "button" || tag === "summary") return "button";
    if (tag === "select") return "combobox";
    if (tag === "textarea") return "textbox";
    if (tag === "form") return "form";
    if (tag === "input") {
      const type = (el.getAttribute("type") || "text").toLowerCase();
      return {
        search: "searchbox", checkbox: "checkbox", radio: "radio",
        submit: "button", button: "button", image: "button", reset: "button",
        range: "slider", number: "spinbutton",
      }[type] || "textbox";
    }
    return undefined;
  };

  const selector = (el) => {
    const parts = [];
    for (let n = el; n && n.nodeType === 1 && parts.length < 4; n = n.parentElement) {
      let part = n.tagName.toLowerCase();
      if (n.id) {
        parts.unshift(part + "#" + CSS.escape(n.id));
        break;
      }
      const cls = [...n.classList].slice(0, 2).map((c) => "." + CSS.escape(c)).join("");
      part += cls;
      const parent = n.parentElement;
      if (parent) {
        const same = [...parent.children].filter((c) => c.tagName === n.tagName);
        if (same.length > 1) part += `:nth-of-type(${same.indexOf(n) + 1})`;
      }
      parts.unshift(part);
    }
    return parts.join(" > ");
  };

  const secret = (el) => {
    const type = (el.getAttribute && el.getAttribute("type") || "").toLowerCase();
    if (type === "password" || type === "hidden") return true;
    if (SECRET_AUTOCOMPLETE.test(el.getAttribute && el.getAttribute("autocomplete") || "")) return true;
    return SECRET_NAME.test((el.name || "") + " " + (el.id || ""));
  };

  const describe = (el) => {
    if (!el || el.nodeType !== 1) return undefined;
    const d = {
      tag: el.tagName.toLowerCase(),
      role: el.getAttribute("role") || implicitRole(el),
      id: el.id || undefined,
      name: el.getAttribute("name") || undefined,
      type: el.getAttribute("type") || undefined,
      label: clip(el.getAttribute("aria-label") || el.getAttribute("title") || el.getAttribute("alt"), MAX_TEXT),
      placeholder: clip(el.getAttribute("placeholder"), MAX_TEXT),
      selector: selector(el),
    };
    if ((!("value" in el) || d.tag === "button") && d.tag !== "form") d.text = clip(el.innerText || el.textContent, MAX_TEXT);
    if (d.tag === "a" && el.href) d.href = el.href;
    return d;
  };

  const valueOf = (el) => {
    if (!el || !("value" in el) || el.tagName === "BUTTON") return {};
    const v = String(el.value ?? "");
    if (secret(el)) return { redacted: true, length: v.length };
    if (el.type === "checkbox" || el.type === "radio") return { checked: !!el.checked };
    return { value: v.length > MAX_VALUE ? v.slice(0, MAX_VALUE) + "…" : v };
  };

  const interactive = (el) =>
    (el && el.closest && el.closest("a[href],button,input,select,textarea,summary,label,[role],[onclick],[tabindex]")) || el;

  const emit = (type, el, extra) => {
    try {
      send(JSON.stringify(Object.assign({
        type,
        url: location.href,
        time: performance.timeOrigin + performance.now(),
        target: describe(el),
      }, extra)));
    } catch (_) {}
  };

  // Typing is reported once it pauses, not per keystroke.
  const pendingInput = new Map();
  const flushInput = (el) => {
    const t = pendingInput.get(el);
    if (t === undefined) return;
    clearTimeout(t);
    pendingInput.delete(el);
    emit("input", el, valueOf(el));
  };

  addEventListener("click", (e) => emit("click", interactive(e.target)), true);
  addEventListener("input", (e) => {
    const el = e.target;
    clearTimeout(pendingInput.get(el));
    pendingInput.set(el, setTimeout(() => flushInput(el), 600));
  }, true);
  addEventListener("change", (e) => {
    flushInput(e.target);
    emit("change", e.target, valueOf(e.target));
  }, true);
  addEventListener("keydown", (e) => {
    if (e.key !== "Enter") return;
    flushInput(e.target);
    emit("enter", e.target, valueOf(e.target));
  }, true);
  addEventListener("submit", (e) => {
    const form = e.target;
    for (const el of pendingInput.keys()) flushInput(el);
    const fields = [...(form.elements || [])]
      .filter((el) => el.name && el.type !== "submit" && el.type !== "button")
      .map((el) => Object.assign({ name: el.name, type: el.type || undefined }, valueOf(el)));
    emit("submit", form, {
      action: form.action || undefined,
      method: (form.method || "get").toUpperCase(),
      fields,
    });
  }, true);
})();
