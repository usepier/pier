// What's typed in pier is code, commands, branch names and prompts: no
// field corrects spelling, capitalizes or suggests. (The native side turns
// off macOS's own substitutions; this covers what WebKit does per field.)
// Every field, the editor's and the terminal's included, as they appear.

const fields = "input, textarea, [contenteditable]";

function plain(el: Element) {
  el.setAttribute("spellcheck", "false");
  el.setAttribute("autocorrect", "off");
  el.setAttribute("autocapitalize", "off");
  if (!el.hasAttribute("autocomplete")) el.setAttribute("autocomplete", "off");
}

export function plainInput() {
  document.documentElement.spellcheck = false;
  for (const el of document.querySelectorAll(fields)) plain(el);
  new MutationObserver((changes) => {
    for (const c of changes) {
      if (c.type === "attributes") {
        if (c.target instanceof Element && c.target.matches(fields)) plain(c.target);
        continue;
      }
      for (const n of c.addedNodes) {
        if (!(n instanceof Element)) continue;
        if (n.matches(fields)) plain(n);
        for (const el of n.querySelectorAll(fields)) plain(el);
      }
    }
  }).observe(document.body, {
    childList: true,
    subtree: true,
    attributes: true,
    attributeFilter: ["contenteditable"],
  });
}
