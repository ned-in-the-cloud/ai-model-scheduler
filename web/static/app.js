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
