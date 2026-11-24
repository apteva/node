// On-screen keyboard. The kiosk has no physical keyboard, so every text
// field marked data-osk gets this one. It docks at the bottom and sets
// --osk-h so overlays shrink above it. A physical keyboard keeps working.

type Layout = "lower" | "upper" | "num";

const rows: Record<Layout, string[][]> = {
  lower: [
    ["q", "w", "e", "r", "t", "y", "u", "i", "o", "p"],
    ["a", "s", "d", "f", "g", "h", "j", "k", "l"],
    ["shift", "z", "x", "c", "v", "b", "n", "m", "back"],
  ],
  upper: [
    ["Q", "W", "E", "R", "T", "Y", "U", "I", "O", "P"],
    ["A", "S", "D", "F", "G", "H", "J", "K", "L"],
    ["shift", "Z", "X", "C", "V", "B", "N", "M", "back"],
  ],
  num: [
    ["1", "2", "3", "4", "5", "6", "7", "8", "9", "0"],
    ["-", "/", ":", ";", "(", ")", "@", "&", "\"", "_"],
    ["#", "=", ".", ",", "?", "!", "'", "+", "%", "back"],
  ],
};

const backIcon = `<svg viewBox="0 0 24 24" width="22" height="22" aria-hidden="true"><path d="M9 6h10a1 1 0 0 1 1 1v10a1 1 0 0 1-1 1H9l-6-6 6-6z" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linejoin="round"/><path d="M12 10l4 4M16 10l-4 4" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"/></svg>`;
const shiftIcon = `<svg viewBox="0 0 24 24" width="22" height="22" aria-hidden="true"><path d="M12 4l8 8h-4.5v7h-7v-7H4l8-8z" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linejoin="round"/></svg>`;

export class Keyboard {
  private el: HTMLElement;
  private target: HTMLInputElement | null = null;
  private layout: Layout = "lower";
  private oneShot = false; // shift for a single letter

  constructor(el: HTMLElement) {
    this.el = el;
    // Keep focus in the field: never let a key press blur it.
    el.addEventListener("pointerdown", (ev) => {
      ev.preventDefault();
      const key = (ev.target as HTMLElement).closest<HTMLElement>("[data-k]");
      if (key) this.press(key.dataset.k!);
    });
    document.addEventListener("focusin", (ev) => {
      const t = ev.target as HTMLElement;
      if (t instanceof HTMLInputElement && t.dataset.osk !== undefined) this.open(t);
    });
    document.addEventListener("focusout", () => {
      // Focus may move straight to another keyboard field.
      setTimeout(() => {
        const a = document.activeElement;
        if (!(a instanceof HTMLInputElement && a.dataset.osk !== undefined)) this.close();
      }, 0);
    });
  }

  private open(input: HTMLInputElement) {
    this.target = input;
    // Mobile browsers (and the preview's phone emulation) shouldn't pop their own.
    input.inputMode = "none";
    const kind = input.dataset.osk;
    this.layout = kind === "num" ? "num" : "lower";
    this.oneShot = false;
    this.render();
    this.el.hidden = false;
    document.documentElement.style.setProperty("--osk-h", `${this.el.offsetHeight}px`);
    input.scrollIntoView({ block: "nearest" });
  }

  close() {
    if (this.el.hidden) return;
    this.el.hidden = true;
    this.target = null;
    document.documentElement.style.setProperty("--osk-h", "0px");
  }

  private render() {
    const kind = this.target?.dataset.osk ?? "";
    const url = kind === "url";
    const key = (k: string) => {
      if (k === "shift") return `<button class="k wide${this.layout === "upper" ? " on" : ""}" data-k="shift" aria-label="Shift">${shiftIcon}</button>`;
      if (k === "back") return `<button class="k wide" data-k="back" aria-label="Delete">${backIcon}</button>`;
      return `<button class="k" data-k="${k === '"' ? "&quot;" : k}">${k === '"' ? "&quot;" : k}</button>`;
    };
    const bottom = [
      `<button class="k wide" data-k="layout">${this.layout === "num" ? "ABC" : "123"}</button>`,
      url ? `<button class="k" data-k=".">.</button><button class="k" data-k="/">/</button>` : "",
      `<button class="k space" data-k=" ">${url ? "" : "space"}</button>`,
      url ? `<button class="k wide" data-k=".com">.com</button>` : `<button class="k" data-k=".">.</button>`,
      `<button class="k wide go" data-k="enter">${this.target?.enterKeyHint === "send" ? "Send" : this.target?.enterKeyHint === "next" ? "Next" : "Done"}</button>`,
    ].join("");
    this.el.innerHTML =
      rows[this.layout].map((r) => `<div class="krow">${r.map(key).join("")}</div>`).join("") + `<div class="krow">${bottom}</div>`;
  }

  private press(k: string) {
    const t = this.target;
    if (!t) return;
    switch (k) {
      case "shift":
        this.layout = this.layout === "upper" ? "lower" : "upper";
        this.oneShot = this.layout === "upper";
        return this.render();
      case "layout":
        this.layout = this.layout === "num" ? "lower" : "num";
        return this.render();
      case "back":
        return this.edit((v, s, e) => (s === e ? [v.slice(0, Math.max(0, s - 1)) + v.slice(e), Math.max(0, s - 1)] : [v.slice(0, s) + v.slice(e), s]));
      case "enter": {
        const form = t.form;
        if (form) form.requestSubmit();
        else t.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true }));
        return;
      }
    }
    this.edit((v, s, e) => [v.slice(0, s) + k + v.slice(e), s + k.length]);
    if (this.oneShot) {
      this.oneShot = false;
      this.layout = "lower";
      this.render();
    }
  }

  private edit(fn: (value: string, start: number, end: number) => [string, number]) {
    const t = this.target!;
    // Password and number inputs don't expose a selection; edit at the end.
    let s = t.value.length, e = t.value.length;
    try {
      if (t.selectionStart !== null) (s = t.selectionStart), (e = t.selectionEnd ?? s);
    } catch {}
    const [v, caret] = fn(t.value, s, e);
    if (t.maxLength > 0 && v.length > t.maxLength) return;
    t.value = v;
    try {
      t.setSelectionRange(caret, caret);
    } catch {}
    t.dispatchEvent(new Event("input", { bubbles: true }));
  }
}
