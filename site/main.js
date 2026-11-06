// smif.dev — small, dependency-free.
(() => {
  "use strict";
  const reduce = matchMedia("(prefers-reduced-motion: reduce)").matches;
  const $ = (s, el = document) => el.querySelector(s);
  const $$ = (s, el = document) => [...el.querySelectorAll(s)];

  /* ---------- reveal on scroll ---------- */
  const io = new IntersectionObserver((entries) => {
    for (const e of entries) {
      if (!e.isIntersecting) continue;
      e.target.classList.add("is-in");
      io.unobserve(e.target);
    }
  }, { rootMargin: "0px 0px -8% 0px", threshold: 0.08 });
  $$(".reveal, .card").forEach((el) => {
    const sib = [...el.parentElement.children].filter((c) => c.classList.contains("reveal"));
    const i = sib.indexOf(el);
    if (i > 0) el.style.setProperty("--rd", `${Math.min(i, 6) * 70}ms`);
    io.observe(el);
  });

  /* ---------- writing to another machine ---------- */
  const rights = $("#machines");
  if (rights) {
    const set = (on) => {
      rights.dataset.write = on ? "on" : "off";
      $("[data-flag]", rights).textContent = on ? " --write" : "";
      $$(".switch button", rights).forEach((b) => b.setAttribute("aria-pressed", String((b.dataset.write === "on") === on)));
      $$("[data-needs-write] .can", rights).forEach((p) => {
        p.textContent = on ? "yes" : "no";
        p.classList.toggle("yes", on);
      });
    };
    $$(".switch button", rights).forEach((b) => b.addEventListener("click", () => set(b.dataset.write === "on")));
  }

  /* ---------- nav ---------- */
  const nav = $(".nav");
  const onScroll = () => nav.classList.toggle("is-scrolled", scrollY > 8);
  addEventListener("scroll", onScroll, { passive: true });
  onScroll();
  const links = $$(".nav__links a");
  const spy = new IntersectionObserver((entries) => {
    for (const e of entries) {
      if (!e.isIntersecting) continue;
      links.forEach((a) => a.classList.toggle("is-active", a.getAttribute("href") === `#${e.target.id}`));
    }
  }, { rootMargin: "-45% 0px -50% 0px" });
  links.forEach((a) => { const s = $(a.getAttribute("href")); if (s) spy.observe(s); });

  /* ---------- copy ---------- */
  const copied = $("#copied");
  let copyT;
  $$(".cmd").forEach((c) => {
    const btn = $(".cmd__copy", c);
    const go = async () => {
      try { await navigator.clipboard.writeText(c.dataset.copy); } catch { return; }
      btn?.classList.add("is-done");
      copied.classList.add("is-on");
      clearTimeout(copyT);
      copyT = setTimeout(() => { copied.classList.remove("is-on"); btn?.classList.remove("is-done"); }, 1400);
    };
    btn?.addEventListener("click", go);
  });

  /* ---------- hero video: chapters + callouts ---------- */
  // Times in the hero recording (seconds), read off its frames.
  const scenes = [
    { t: 0.8, k: "wait", g: "!", chap: "waiting", text: "billing asks you a question. smif shows it; the answer stays yours." },
    { t: 2.0, k: "new", g: "●", chap: "new answer", text: "handbook answered while you were away." },
    { t: 3.0, k: "work", g: "▶", chap: "working", text: "infra runs terraform plan and has written nothing for 7 minutes." },
    { t: 4.6, k: "new", g: "●", chap: "tab", text: "payments just finished. tab jumps to the next session that needs you." },
    { t: 7.4, k: "key", g: "p", chap: "saved prompt", text: "p on an idle session: saved prompts. 3 types “run the tests…” into its tab." },
    { t: 8.9, k: "work", g: "▶", chap: "live", text: "ios works. Each tool call shows the moment it starts." },
    { t: 14.6, k: "new", g: "●", chap: "answer", text: "The answer lands: 141 of 142 tests pass, and why the last one fails." },
    { t: 16.6, k: "wait", g: "!", chap: "permission", text: "storefront waits on git push. o brings its tab to the front." },
  ];
  const v = $("#hero-video");
  const frame = v?.closest(".term");
  const callout = $("#callout");
  const chapters = $("#chapters");
  if (v && chapters) {
    const chaps = scenes.map((s, i) => {
      const b = document.createElement("button");
      b.type = "button";
      b.className = "chap";
      b.innerHTML = `<span class="chap__bar"><i></i></span>${s.chap}`;
      b.setAttribute("aria-label", `Jump to: ${s.text}`);
      b.addEventListener("click", () => { v.currentTime = s.t + 0.05; v.play().catch(() => {}); });
      chapters.appendChild(b);
      return b;
    });
    let cur = -1;
    const show = (i) => {
      if (i === cur) return;
      cur = i;
      if (i < 0) { callout.classList.remove("is-on"); return; }
      const s = scenes[i];
      callout.classList.remove("is-on");
      setTimeout(() => {
        if (cur !== i) return;
        callout.dataset.k = s.k;
        $(".callout__glyph", callout).textContent = s.g;
        $(".callout__text", callout).textContent = s.text;
        callout.classList.add("is-on");
      }, 140);
    };
    const tick = () => {
      const t = v.currentTime;
      let i = -1;
      scenes.forEach((s, j) => { if (t >= s.t) i = j; });
      show(i);
      chaps.forEach((b, j) => {
        const start = scenes[j].t, end = j + 1 < scenes.length ? scenes[j + 1].t : v.duration || start + 2;
        const p = Math.max(0, Math.min(1, (t - start) / (end - start)));
        b.classList.toggle("is-on", j === i);
        b.classList.toggle("is-done", t >= end);
        b.querySelector("i").style.width = j === i ? `${p * 100}%` : "";
      });
      if (!v.paused) requestAnimationFrame(tick);
    };
    v.addEventListener("play", () => { frame.classList.remove("is-paused"); requestAnimationFrame(tick); });
    v.addEventListener("pause", () => { frame.classList.add("is-paused"); tick(); });
    v.addEventListener("seeked", tick);
    $("[data-toggle-play]")?.addEventListener("click", (e) => {
      if (v.paused) { v.play().catch(() => {}); e.currentTarget.setAttribute("aria-label", "Pause the recording"); }
      else { v.pause(); e.currentTarget.setAttribute("aria-label", "Play the recording"); }
    });
    // Play while on screen, pause when scrolled away (and never autoplay with reduced motion).
    if (reduce) { frame.classList.add("is-paused"); }
    else {
      new IntersectionObserver(([e]) => { e.isIntersecting ? v.play().catch(() => {}) : v.pause(); }, { threshold: 0.25 }).observe(v);
    }
  }

  // Other recordings: load and play only on screen.
  $$("video[data-inview]").forEach((vid) => {
    if (reduce) { vid.controls = true; return; }
    new IntersectionObserver(([e]) => {
      if (e.isIntersecting) { if (vid.preload === "none") { vid.preload = "auto"; vid.load(); } vid.play().catch(() => {}); }
      else vid.pause();
    }, { threshold: 0.3 }).observe(vid);
  });

  /* ---------- live timers in the state rows ---------- */
  const fmt = (s) => s < 60 ? `${s}s` : s < 3600 ? `${Math.floor(s / 60)}m${String(s % 60).padStart(2, "0")}s` : `${Math.floor(s / 3600)}h${String(Math.floor(s / 60) % 60).padStart(2, "0")}m`;
  const ticks = $$("[data-tick]").map((el) => ({ el, s: +el.dataset.tick }));
  const agos = $$("[data-ago]").map((el) => ({ el, s: +el.dataset.ago }));
  setInterval(() => {
    ticks.forEach((x) => { x.s++; x.el.textContent = fmt(x.s); });
    agos.forEach((x) => { x.s++; x.el.textContent = `${Math.floor(x.s / 60)}m`; });
  }, 1000);

  /* ---------- typing in the new-session card ---------- */
  $$("[data-type]").forEach((el) => {
    const word = el.dataset.type;
    if (reduce) { el.textContent = word; return; }
    let i = 0, dir = 1;
    const step = () => {
      i += dir;
      el.textContent = word.slice(0, i);
      let wait = 110 + Math.random() * 80;
      if (i === word.length) { dir = -1; wait = 2600; }
      else if (i === 0) { dir = 1; wait = 900; }
      else if (dir < 0) wait = 35;
      setTimeout(step, wait);
    };
    setTimeout(step, 1200);
  });

  /* ---------- keys ---------- */
  const keys = $$(".key");
  const desc = $("#keydesc");
  const press = (btn) => {
    keys.forEach((k) => k.classList.remove("is-down"));
    btn.classList.add("is-down");
    setTimeout(() => btn.classList.remove("is-down"), 180);
    desc.innerHTML = `<b>${btn.textContent}</b>${btn.dataset.desc}`;
  };
  keys.forEach((k) => {
    k.addEventListener("click", () => press(k));
    k.addEventListener("mouseenter", () => { desc.innerHTML = `<b>${k.textContent}</b>${k.dataset.desc}`; });
  });
  const keysSec = $("#keys");
  let keysVisible = false;
  new IntersectionObserver(([e]) => { keysVisible = e.isIntersecting; }, { threshold: 0.4 }).observe(keysSec);
  addEventListener("keydown", (e) => {
    if (!keysVisible || e.metaKey || e.ctrlKey || e.altKey) return;
    if (/^(INPUT|TEXTAREA)$/.test(document.activeElement?.tagName)) return;
    const k = keys.find((b) => b.dataset.key === e.key);
    if (!k) return;
    // Tab keeps moving focus as usual; arrows only stop scrolling while focus is on the keys.
    if (e.key.startsWith("Arrow") && document.activeElement?.closest(".keys__board")) e.preventDefault();
    press(k);
  });

  /* ---------- install tabs ---------- */
  const tabs = $$(".install__tabs [role=tab]");
  tabs.forEach((t) => t.addEventListener("click", () => {
    tabs.forEach((x) => { x.setAttribute("aria-selected", x === t); $(`#${x.getAttribute("aria-controls")}`).hidden = x !== t; });
  }));
})();
