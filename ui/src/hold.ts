// Press-and-hold buttons, for decisions with real-world consequences
// (opening a door, erasing the device). A stray tap does nothing; the fill
// shows how long is left. On completion the button fires a "held" event.
//
//   <button class="btn hold" data-hold-ms="1200"><span class="hold-fill"></span><span>Hold to allow</span></button>

export function holdButton(label: string, attrs: string, ms = 1200, cls = "primary"): string {
  return `<button class="btn hold ${cls}" data-hold-ms="${ms}" ${attrs}><span class="hold-fill"></span><span class="hold-label">${label}</span></button>`;
}

let active: { el: HTMLElement; timer: ReturnType<typeof setTimeout> } | null = null;

function cancel() {
  if (!active) return;
  clearTimeout(active.timer);
  active.el.classList.remove("holding");
  active = null;
}

document.addEventListener("pointerdown", (ev) => {
  const el = (ev.target as HTMLElement).closest<HTMLElement>("[data-hold-ms]");
  if (!el || (el as HTMLButtonElement).disabled) return;
  cancel();
  const ms = Number(el.dataset.holdMs) || 1200;
  el.style.setProperty("--hold-ms", `${ms}ms`);
  el.classList.add("holding");
  try {
    el.setPointerCapture(ev.pointerId);
  } catch {}
  active = {
    el,
    timer: setTimeout(() => {
      el.classList.remove("holding");
      active = null;
      el.dispatchEvent(new CustomEvent("held", { bubbles: true }));
    }, ms),
  };
});
document.addEventListener("pointerup", cancel);
document.addEventListener("pointercancel", cancel);
