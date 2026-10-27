// Package mailguard holds the abuse-control primitives shared by urMail's
// mail protocol boundaries: client source identification behind trusted
// proxies and a per-source authentication failure limiter.
//
// Both primitives are pure Go with injected clock/resolver dependencies and
// no global mutable state, so they can be unit tested deterministically and
// used from more than one process.
//
// # Persistence
//
// The limiter keeps bans in memory only. That is deliberate: a ban is a
// short-lived (never permanent) throttle, not durable authorisation state.
// The protection *policy* is persisted by the generated Postfix and Dovecot
// configuration (anvil rate limits, smtpd error limits, auth_failure_delay,
// connection budgets), which is re-rendered from the database snapshot and
// re-applied on every service restart. A restart therefore drops in-flight
// bans but immediately reinstates the same thresholds. Durable rules such as
// From-authorization maps, relay rejection and the recipient/quota policy
// service live in Postfix configuration and are never derived from this
// package's memory.
package mailguard
