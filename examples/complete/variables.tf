variable "bucket_name" {
  description = "The bucket's name. Bucket names are shared by every account: pick one of your own, like shop-files-acme."
  type        = string
}

variable "domain" {
  description = "A domain of yours for the site, like shop.example.com. Create the DNS records in the dns_records output."
  type        = string
}

variable "datadog_api_key" {
  description = "Datadog's API key, where the project's logs go."
  type        = string
  sensitive   = true
}

variable "protect" {
  description = "Protect the project, the database and the bucket from deletion. Keep it on for anything that matters: to destroy them, set it to false and apply first."
  type        = bool
  default     = true
}
