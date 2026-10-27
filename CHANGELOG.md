# Changelog

All notable changes to this provider are written here, newest first. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow [Semantic Versioning](https://semver.org).

## 0.1.0 (Unreleased)

The first release, over the whole public API v1.

### Resources

- Compute: `masterdaweb_instance` (ordered from a subscription's balance or by invoice, waited for until it is
  built; renamed, started and stopped, its password reset, resized — plan, main disk, data disks, add-ons, extra
  addresses — and reinstalled from another image in place, the resize quoted at plan time),
  `masterdaweb_ssh_key`, `masterdaweb_snapshot`, `masterdaweb_backup_schedule`.
- Networking: `masterdaweb_firewall` (rules in order, committed onto exactly the instances listed),
  `masterdaweb_vpc` (with its members' private addresses), `masterdaweb_vpc_attachment` (at a private address of
  your choice), `masterdaweb_reverse_dns`.
- DNS: `masterdaweb_dns_zone` (with the nameservers to set at the registrar), `masterdaweb_dns_record` (a whole
  record set).
- Projects: `masterdaweb_project` (what groups instances, databases, buckets and services),
  `masterdaweb_project_resource`.
- Serverless: `masterdaweb_serverless_service`, `masterdaweb_serverless_variable`, `masterdaweb_serverless_domain`,
  `masterdaweb_serverless_binding`, `masterdaweb_serverless_log_drain`.
- Data: `masterdaweb_database`, `masterdaweb_object_storage_bucket`, `masterdaweb_object_storage_key`.
- Account: `masterdaweb_webhook`.
- VPN: `masterdaweb_vpn_user` (a login with the VPN app, applied to the gateway in place after each change; a
  generated password kept in the state), `masterdaweb_vpn_gateway_settings` (an existing gateway's transport,
  address pool, routing, shared logins and MTU, applied and waited for; a rebuild only with `allow_rebuild`; changes
  waiting on the gateway are applied on the next run).
- Security: `masterdaweb_security_exception` (a finding, a check or a port accepted with a justification until a
  date, capped by the platform; revoked on destroy; a lapsed one is proposed again), `masterdaweb_security_settings`.

### Data sources

- `masterdaweb_regions`, `masterdaweb_plans`, `masterdaweb_images`, `masterdaweb_instance`, `masterdaweb_instances`,
  `masterdaweb_ssh_key`, `masterdaweb_vpc`, `masterdaweb_firewall`, `masterdaweb_firewall_macros`,
  `masterdaweb_dns_zone`, `masterdaweb_project`, `masterdaweb_serverless_outbound_addresses`,
  `masterdaweb_vpn_gateway`, `masterdaweb_vpn_gateways`, `masterdaweb_security_overview`,
  `masterdaweb_security_findings`.

### Behaviour

- Every resource can be imported.
- Waits for asynchronous work (orders, builds, resizes, reinstalls, firewall commits, VPC attachments, deletions)
  within configurable `timeouts`, and while another operation holds an instance.
- A resize the subscription's balance does not cover stops before anything changes, saying what is short
  (`shortfall_cents`).
- Retries 429, 502, 503 and 504 (a 500 for reads only) after the wait the API asks for (`Retry-After`), with the
  same `Idempotency-Key` on every write.
- The API's refusals (RFC 9457 problem details) are shown as it says them, with the field and the request id.
- Works with Terraform 1.0+ and OpenTofu (plugin protocol 6).
