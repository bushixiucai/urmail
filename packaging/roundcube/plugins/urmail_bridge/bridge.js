/* Browser actions contain references only; the authenticated server reads mail. */
(function () {
  "use strict";
  const requests = new Map();
  rcmail.addEventListener("init", function () {
    const labels = rcmail.env.urmail_labels || {};
    ["classify", "summarize", "translate", "phishing", "polish"].forEach(function (operation) {
      rcmail.register_command("plugin.urmail_" + operation, function () {
        const uid = rcmail.get_single_uid();
        if (!uid) { rcmail.display_message(labels.select_message, "error"); return; }
        const language = operation === "translate" ? window.prompt(labels.language, "en") : "";
        if (language === null) return;
        const policy = rcmail.env.urmail_ai_policy || {};
        if (!policy.allowed || !policy.configuration_digest) { rcmail.display_message(labels.authorization_unavailable, 'error'); return; }
        if (policy.requires_confirmation && !window.confirm(labels.cloud_confirm + '\n' + policy.provider + ' · ' + policy.model + '\n' + policy.endpoint + '\n' + labels.cloud_scope)) return;
        const identity = JSON.stringify([rcmail.env.mailbox, uid, operation, language, policy.configuration_digest]);
        if (!requests.has(identity)) requests.set(identity, window.crypto.randomUUID());
        rcmail.http_post("plugin.urmail_context", {_uid: uid, _mbox: rcmail.env.mailbox,
          _operation: operation, _language: language, _request_key: requests.get(identity), _configuration_digest: policy.configuration_digest, _cloud_confirmed: policy.requires_confirmation ? "1" : "0"}, rcmail.set_busy(true));
      }, true);
    });
    rcmail.register_command("plugin.urmail_sieve", function () {
      rcmail.http_post("plugin.urmail_sieve", {}, rcmail.set_busy(true));
    }, true);
  });
  rcmail.addEventListener("plugin.urmail_result", function (result) {
    const dialog = document.createElement("dialog");
    const title = document.createElement("p");
    title.textContent = rcmail.env.urmail_labels.suggestion;
    const output = document.createElement("pre");
    output.textContent = JSON.stringify(result, null, 2);
    output.style.whiteSpace = "pre-wrap";
    output.style.maxWidth = "min(70vw, 48rem)";
    const close = document.createElement("button");
    close.type = "button"; close.textContent = "×";
    close.addEventListener("click", function () { dialog.close(); dialog.remove(); });
    dialog.append(title, output, close); document.body.append(dialog); dialog.showModal();
  });
  rcmail.addEventListener("plugin.urmail_sieve_ticket", function (issued) {
    const form = document.createElement("form");
    form.method = "POST"; form.action = "/api/v1/mailbox/sieve/exchange";
    const input = document.createElement("input");
    input.type = "hidden"; input.name = "ticket"; input.value = issued.ticket;
    form.append(input); document.body.append(form); form.submit();
  });
}());
