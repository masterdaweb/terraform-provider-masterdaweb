resource "masterdaweb_project" "shop" {
  name        = "shop"
  description = "The shop's site, API and workers"

  # On by default. To destroy the project, set it to false and apply first.
  deletion_protection = true

  # Where it runs, for good: a region's id (the masterdaweb_regions data
  # source). Left out: the platform's default location.
  # region_id = "3"
}
