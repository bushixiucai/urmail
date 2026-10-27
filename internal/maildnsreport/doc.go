// Package maildnsreport implements MAIL-008: mail transport security policy
// publication and reporting. Given a domain's current DNS state it produces
//
//   - the required DNS records (MTA-STS TXT, _smtp._tls TLS-RPT, SPF, DKIM,
//     DMARC) as a plan the dnsprovider layer can apply,
//   - a completeness report classifying each requirement as ok / missing /
//     present_wrong with reasons, and
//   - the MTA-STS well-known policy body served at
//     https://mta-sts.<domain>/.well-known/mta-sts.txt.
//
// Everything in this package is pure: no DNS queries, no filesystem, no
// network. The caller supplies a snapshot of observed records.
//
// Wiring (handlers are owned by another workstream): expose
//
//	Service.DomainReport(ctx, tenantID, domain)
//
// through the policy service composition and mount the JSON result at
//
//	GET /api/v1/tenants/{tenant_id}/domains/{domain}/dns-policy-report
//
// in internal/api/router.go. Until that mount lands, CLI and reconcile loops
// consume the Service directly. MTA-STS policy identity and TLS-RPT report
// URI are persisted in the domain_transport_security table (migration 0124)
// so the well-known body is stable across renders.
//
// Scope guard (FUNCTIONAL_SCOPE): DANE/TLSA (DNS-08) and SSHFP (DNS-11) stay
// DEFERRED and are deliberately NOT checked or published here; this package
// is report-only for them.
package maildnsreport
