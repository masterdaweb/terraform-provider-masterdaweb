output "url" {
  description = "The site's own address, live once its first deployment is."
  value       = masterdaweb_serverless_service.web.url
}

output "dns_records" {
  description = "The records to create at your DNS provider for the domain."
  value       = masterdaweb_serverless_domain.site.dns_records
}

output "database_host" {
  description = "Where the database answers (the service reaches it through its binding)."
  value       = masterdaweb_database.db.host
}

output "database_password" {
  description = "The database owner's password, for your own tools (the service has a login of its own)."
  value       = masterdaweb_database.db.password
  sensitive   = true
}
