# DNSSEC key rollover runbook: signed-example.com

Old key: tag 23417 (flags 257, algorithm 13, state signing)
New key: tag 51993 (flags 257, algorithm 13, state published)

Execute the steps in order. Do not skip the TTL waits: cached resolvers
will fail validation otherwise.

## 1. Publish the new DNSKEY

Move the new key (tag 51993) to the published state and reload so the reconcile loop renders it into the zone DNSKEY RRset. Verify with: dig DNSKEY signed-example.com +short | wc -l

## 2. Wait for DNSKEY propagation

Wait at least the DNSKEY TTL (3600 seconds) so every resolver that cached the RRset sees the new key before any signature depends on it.

Wait: 3600 seconds.

## 3. Publish the new DS at the parent

Give the new DS record to the parent zone / registrar: signed-example.com IN DS 51993 13 2 00FEDCBA

Wait: 3600 seconds.

## 4. Sign with the new key (double-sign window)

Transition the new key (tag 51993) to the signing state so the zone carries signatures from both keys, then re-sign the zone. Keep double-signing for at least one signature refresh cycle.

## 5. Withdraw the old DS at the parent

Remove the old DS for key tag 23417 from the parent zone and wait the TTL again before retiring the key.

Wait: 3600 seconds.

## 6. Retire the old key

Transition the old key (tag 23417) to the retiring state and re-sign; the old key stays in the DNSKEY RRset but stops producing signatures.

Wait: 3600 seconds.

## 7. Remove the old key

After the retirement wait, transition the old key (tag 23417) to the removed state, reload, and archive or delete its private key from the key directory. Verify validation with: dig +dnssec signed-example.com SOA

