// Copy-to-clipboard for endpoint values. Falls back to execCommand for
// non-secure contexts (plain http over the LAN), where navigator.clipboard
// is unavailable.
function copyText(btn) {
  const text = btn.dataset.copy;
  const done = () => {
    const old = btn.textContent;
    btn.textContent = "✓";
    setTimeout(() => { btn.textContent = old; }, 1200);
  };
  if (navigator.clipboard && window.isSecureContext) {
    navigator.clipboard.writeText(text).then(done);
  } else {
    const ta = document.createElement("textarea");
    ta.value = text;
    ta.style.position = "fixed";
    ta.style.opacity = "0";
    document.body.appendChild(ta);
    ta.select();
    document.execCommand("copy");
    ta.remove();
    done();
  }
}

// Deployment log viewer. While scrolled to the bottom it follows the tail;
// scrolling up pauses polling so the text holds still, and "Jump to latest"
// (or scrolling back down) resumes. Each poll swaps in a fresh <pre>, so the
// scroll position is re-applied after every swap.
const logView = { follow: true };

function logPre() {
  return document.querySelector("#logs pre.logs");
}

function setLogFollow(on) {
  logView.follow = on;
  const btn = document.querySelector("#logs .logs-resume");
  if (btn) btn.hidden = on;
}

function followLogs() {
  setLogFollow(true);
  const pre = logPre();
  if (pre) pre.scrollTop = pre.scrollHeight;
  htmx.trigger("#logs", "logs-refresh");
}

// scroll doesn't bubble, so listen in the capture phase.
document.addEventListener("scroll", (e) => {
  const el = e.target;
  if (!(el instanceof Element) || !el.matches("#logs pre.logs")) return;
  setLogFollow(el.scrollHeight - el.scrollTop - el.clientHeight < 24);
}, true);

document.addEventListener("htmx:beforeRequest", (e) => {
  const elt = e.detail.elt;
  if (elt.closest("#logs .tabs")) {
    setLogFollow(true); // switching streams starts at the tail
  } else if (elt.id === "logs" && !logView.follow) {
    e.preventDefault(); // paused: skip this poll
  }
});

document.addEventListener("htmx:afterSwap", () => {
  const pre = logPre();
  if (!pre) return;
  if (logView.follow) pre.scrollTop = pre.scrollHeight;
  setLogFollow(logView.follow);
});
