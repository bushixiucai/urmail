## AXFR credential handling (TSIG)

Zone transfers between the hidden primary and the secondary (ns2.urmail.cn) are
authenticated with a shared TSIG key named axfr-urmail (algorithm hmac-sha256).

### Provisioning

1. Generate the secret offline on the primary: `ddns-confgen -a hmac-sha256 -k axfr-urmail -q` (or `tsig-keygen`).
2. Store the key block ONLY in /etc/nsd/tsig.keys on both nodes,
   owner root:nsd, mode 0600. Never commit it, never store it in the
   database, and never pass it through the render pipeline: urmaild
   renders the key NAME only.
3. Include it from both nsd.conf files: `include: /etc/nsd/tsig.keys`.
4. Restrict every provide-xfr/request-xfr to the key name; never allow
   unauthenticated AXFR by address alone.

### Rotation

1. Generate a successor key with a NEW name (e.g. axfr-urmail-2).
2. Publish both keys on both nodes; switch the rendered notify/xfr
   references to the new name.
3. After one refresh+retry window, remove the old key from both key
   files and reload nsd-control on each node.

### Audit

- Confirm mode 0600 and root:nsd ownership on /etc/nsd/tsig.keys.
- Check nsd.log for AXFR denied events from unkeyed peers.
- The secret must never appear in rendered output, config backups, or
  this runbook output; rotate immediately if it leaks.
