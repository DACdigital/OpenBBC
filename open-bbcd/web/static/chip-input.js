// chip-input.js — turns <div data-chip-input> into a tag input.
// Markup contract:
//   <div class="chip-input" data-chip-input data-name="user_phrases" data-value="a,b,c"></div>
// Renders chips, an inline text field, and a hidden input named data-name carrying
// the current comma-joined value. Triggers a 'change' on the hidden input on every
// add/remove so htmx form serialization picks up the latest value.

(function () {
  function init(el) {
    if (el.dataset.chipReady === "1") return;
    el.dataset.chipReady = "1";

    const name = el.dataset.name || "phrases";
    const initial = (el.dataset.value || "")
      .split(",")
      .map((s) => s.trim())
      .filter(Boolean);

    const hidden = document.createElement("input");
    hidden.type = "hidden";
    hidden.name = name;
    el.appendChild(hidden);

    const input = document.createElement("input");
    input.type = "text";
    input.className = "chip-input-field";
    input.placeholder = "type a phrase, press Enter";
    el.appendChild(input);

    let chips = [];

    function sync() {
      hidden.value = chips.join(",");
      hidden.dispatchEvent(new Event("change", { bubbles: true }));
      el.querySelectorAll(".chip").forEach((c) => c.remove());
      chips.forEach((value, idx) => {
        const span = document.createElement("span");
        span.className = "chip";
        span.textContent = value;
        const x = document.createElement("button");
        x.type = "button";
        x.className = "chip-x";
        x.textContent = "×";
        x.onclick = () => {
          chips.splice(idx, 1);
          sync();
        };
        span.appendChild(x);
        el.insertBefore(span, input);
      });
    }

    function addFromInput() {
      const v = input.value.trim();
      if (v && !chips.includes(v)) {
        chips.push(v);
      }
      input.value = "";
      sync();
    }

    input.addEventListener("keydown", (e) => {
      if (e.key === "Enter" || e.key === ",") {
        e.preventDefault();
        addFromInput();
      } else if (e.key === "Backspace" && input.value === "" && chips.length) {
        chips.pop();
        sync();
      }
    });
    input.addEventListener("blur", addFromInput);

    chips = initial;
    sync();
  }

  function scan(root) {
    (root || document).querySelectorAll("[data-chip-input]").forEach(init);
  }

  document.addEventListener("DOMContentLoaded", () => scan());
  // Re-scan after htmx swaps so newly-rendered detail panes get wired.
  document.body.addEventListener("htmx:afterSwap", (e) => scan(e.target));
})();
