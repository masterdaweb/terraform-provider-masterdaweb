---
page_title: "masterdaweb_instance Resource - masterdaweb"
subcategory: "Compute"
description: |-
  A cloud instance (VPS), ordered through the API like in the checkout: from a subscription's balance (subscription_id with payment = "balance" is the unattended way), or with an invoice Terraform waits for. Terraform waits until the instance is built and running. Changing its image, SSH keys, user data or app inputs reinstalls it in place (its disk is replaced; its addresses, plan and billing stay), and the plan warns about it.
---

# masterdaweb_instance (Resource)

A cloud instance (VPS), ordered through the API like in the checkout: from a subscription's balance (subscription_id with payment = "balance" is the unattended way), or with an invoice Terraform waits for. Terraform waits until the instance is built and running. Changing its image, SSH keys, user data or app inputs reinstalls it in place (its disk is replaced; its addresses, plan and billing stay), and the plan warns about it.

## How changes apply

| Change | What happens |
| --- | --- |
| `name`, `description`, `power_state`, `project_id` | In place (`project_id = ""` takes it out of its project; left out, the project is not managed here). |
| `password` | Reset on the running instance, through its guest agent. |
| `plan`, `main_disk_extra_gb`, `extra_disks_gb`, `addons`, `extra_ipv4`, `extra_ipv6` | The instance is **resized in place**, like DigitalOcean's `size` or Vultr's `plan`. The plan quotes it: what it costs now and per hour after, whether it restarts the instance (a CPU, memory or disk change does), and what it removes. |
| `image`, `custom_image_id`, `ssh_key_ids`, `user_data`, `app_inputs` | The instance is **reinstalled in place**: everything on its disk is replaced, its addresses, plan and billing stay. The plan warns about it. |
| `term`, `subscription_id` | Refused at plan time: they belong to the subscription. |
| `region_id`, `network_mode`, a set `vpc_id` | The instance is replaced (a new order). |

## Resizing

- **Plans.** Any plan the instance can move to, up or down: an upgrade on a commitment is charged now for the rest of
  the cycle, a downgrade gives the difference back as credit. When the subscription's balance does not cover it, the
  plan says what is short and the apply stops before anything changes (`shortfall_cents`): top the balance up and
  apply again.
- **The main disk only grows.** A smaller `main_disk_extra_gb`, or a smaller plan with a disk that would shrink, is
  refused at plan time. Left out, `main_disk_extra_gb` is what the instance has, and a plan change keeps the disk's
  size. After a disk grows, grow the partition inside the guest.
- **Data disks** are kept in the order of `extra_disks_gb`: disks added at the end are added, disks taken off the end
  are removed **with all their data** (the plan warns), and a disk's size never changes.
- **Add-ons** listed in `addons` are set to their quantity; one taken out of the list is removed where it may be.
  Add-ons the list never had are left alone.
- **Addresses.** Raising `extra_ipv4` or `extra_ipv6` adds public addresses; lowering it releases the newest ones
  that are not the primary, for good.
- What the API would refuse (a plan no longer sold, an add-on only ever raised, a disk too small) is refused at plan
  time with its reason.

## Ordering and payment

An instance is ordered like in the checkout. With `subscription_id` and `payment = "balance"` it is paid from
that subscription's balance and built at once: the unattended way, for pipelines. Without a subscription the
order starts a new one, which is always paid by invoice: Terraform waits for the invoice to be paid (up to the
`create` timeout) and logs where to pay it. If the timeout passes first, pay the invoice and import the instance
once it is built: applying again would order another.

Terraform then waits until the instance is built and running. Destroying it terminates it at once and stops its
billing.

## Example Usage

```terraform
variable "subscription_id" {
  description = "A subscription of the account, whose balance pays for the instance."
  type        = string
}

data "masterdaweb_regions" "open" {
  available = true
}

data "masterdaweb_plans" "medium" {
  term       = "MONTHLY"
  min_vcpus  = 2
  min_ram_mb = 4096
}

resource "masterdaweb_ssh_key" "laptop" {
  name       = "laptop"
  public_key = file("~/.ssh/id_ed25519.pub")
}

resource "masterdaweb_instance" "web" {
  name        = "web-1"
  description = "The shop's web server"
  region_id   = data.masterdaweb_regions.open.regions[0].id
  plan        = data.masterdaweb_plans.medium.plans[0].code
  image       = "ubuntu-24-04"

  # Unattended: paid from the subscription's balance, never by invoice.
  subscription_id = var.subscription_id
  payment         = "balance"

  ssh_key_ids = [masterdaweb_ssh_key.laptop.id]
  user_data   = <<-EOT
    #cloud-config
    packages: [nginx]
  EOT

  # Changed in place: a resize, quoted at plan time.
  main_disk_extra_gb = 40
  extra_disks_gb     = [200]
  extra_ipv4         = 1
  addons             = [{ code = "backup-slot", quantity = 2 }]

  timeouts = {
    create = "45m"
  }
}

output "web_ipv4" {
  value = masterdaweb_instance.web.ipv4_address
}
```

<!-- schema generated by tfplugindocs -->
## Schema

### Required

- `plan` (String) The plan, by its code (the masterdaweb_plans data source). Changing it resizes the instance in place, quoted first: the plan says what it costs and removes. A CPU or memory change powers it off and on again.
- `region_id` (String) The region, by id (the masterdaweb_regions data source). Changing it replaces the instance.

### Optional

- `addons` (Attributes Set) The add-ons Terraform manages (the catalog's, like backup slots), changed in place: one taken out of the list is removed where it may be. Add-ons the list never had are left as they are. (see [below for nested schema](#nestedatt--addons))
- `app_inputs` (Map of String, Sensitive) Answers to the app's setup questions, by key (the image's inputs in the masterdaweb_images data source). Changing it reinstalls the instance in place: everything on its disk is replaced (its addresses, plan and billing stay).
- `custom_image_id` (String) One of your own uploaded disk images, by id, instead of image. It brings its own cloud-init: ssh_key_ids, user_data and app_inputs do not go with it. Changing it reinstalls the instance in place: everything on its disk is replaced (its addresses, plan and billing stay).
- `description` (String) A note on the instance, up to 500 characters. Changed in place.
- `extra_disks_gb` (List of Number) Extra data disks, by size in GB, in order. Disks added at the end of the list are added in place; disks taken off the end are removed with ALL their data; a disk's size never changes. Left out, they are what the instance has (a disk added elsewhere shows as 0, its size unknown); [] removes them all.
- `extra_ipv4` (Number) Public IPv4 addresses beyond the plan's. Raising it adds addresses in place; lowering it releases the newest ones that are not the primary (gone for good). Left out, it is what the instance has.
- `extra_ipv6` (Number) Public IPv6 addresses beyond the plan's, as extra_ipv4.
- `image` (String) The operating system or app to install, by its code (the masterdaweb_images data source). Give image or custom_image_id. Changing it reinstalls the instance in place: everything on its disk is replaced (its addresses, plan and billing stay).
- `initial_credit_cents` (String) A new ON_DEMAND subscription's first prepaid credit, in cents (at least the minimum recharge, its default). Used when the instance is created; changing it later changes nothing.
- `main_disk_extra_gb` (Number) GB on the main disk beyond the plan's. Raising it grows the disk in place (then grow the partition inside the guest); the main disk never shrinks, and a smaller value is refused at plan time. Left out, it is what the instance has.
- `name` (String) The instance's display name, up to 120 characters. Changed in place.
- `network_mode` (String) public (public addresses) or private (no public address: on vpc_id only). Changing it replaces the instance.
- `password` (String, Sensitive) root's (Administrator's) password, for images that take one: 8 to 200 letters, digits and US-keyboard symbols. Changing it resets the password on the running instance (through its guest agent); a reinstall builds the instance with it.
- `payment` (String) How the order is paid: auto (the subscription's balance when it covers it, else an invoice), balance (never an invoice: refused when the balance is short; needs subscription_id) or invoice. Used when the instance is created; changing it later changes nothing.
- `power_state` (String) running or stopped. Changing it starts or stops the instance.
- `project_id` (String) The project the instance is in (masterdaweb_project): it joins it when it is created, and changing it moves it; "" takes it out of its project. The instance itself is not touched. Left out, it is the project the instance is in (managed elsewhere, like with masterdaweb_project_resource).
- `ssh_key_ids` (Set of String) Up to 20 SSH keys (masterdaweb_ssh_key) root accepts, put on the instance when it is built, on Linux catalog images. Changing it reinstalls the instance in place: everything on its disk is replaced (its addresses, plan and billing stay).
- `subscription_id` (String) An existing subscription to add the instance to. Without one, the order starts a new subscription, which is always paid by invoice. It cannot change after the instance is created.
- `term` (String) The billing term: MONTHLY, QUARTERLY, SEMI_ANNUAL, ANNUAL or ON_DEMAND (hourly, from prepaid credit). An existing subscription's term wins. It cannot change after the instance is created.
- `timeouts` (Attributes) How long Terraform waits, per operation, as a duration like "30m" or "1h". (see [below for nested schema](#nestedatt--timeouts))
- `user_data` (String) User data for the first boot, on Linux catalog images: a #cloud-config document or a #! script, up to 16 KiB, merged into the image's own cloud-init (the root password, the hostname and disk growth are the platform's, and refused). Never stored in the clear. Changing it reinstalls the instance in place: everything on its disk is replaced (its addresses, plan and billing stay).
- `vpc_id` (String) The VPC the instance is on. Set it to create the instance on that VPC (required with network_mode = "private"); left out, it shows a VPC the instance was put on afterwards (masterdaweb_vpc_attachment). Changing a set value replaces the instance.

### Read-Only

- `created_at` (String) When the instance was created (RFC 3339).
- `disk_gb` (Number) Its main disk, in GB.
- `hostname` (String) The instance's hostname.
- `id` (String) The instance's id.
- `ip_addresses` (Attributes List) Every address of the instance. (see [below for nested schema](#nestedatt--ip_addresses))
- `ipv4_address` (String) The primary public IPv4 address, when it has one.
- `ipv6_address` (String) The primary IPv6 address, when it has one.
- `order_id` (String) The order the instance was bought with (null for an imported one).
- `ram_mb` (Number) Its memory, in MB.
- `region_name` (String) The region's name.
- `status` (String) The live power state (running, stopped…), or suspended while suspended for non-payment.
- `vcpus` (Number) Its vCPUs.

<a id="nestedatt--addons"></a>
### Nested Schema for `addons`

Required:

- `code` (String) The add-on's code.

Optional:

- `quantity` (Number) How many; the add-on's minimum when left out.


<a id="nestedatt--timeouts"></a>
### Nested Schema for `timeouts`

Optional:

- `create` (String) How long to wait for the order to be paid and the instance built and running (default 60m).
- `delete` (String) How long to wait for the instance to be gone (default 20m).
- `update` (String) How long to wait for a resize, a reinstall or a power change (default 30m).


<a id="nestedatt--ip_addresses"></a>
### Nested Schema for `ip_addresses`

Read-Only:

- `address` (String) The address.
- `primary` (Boolean) Whether it is the primary address.
- `version` (String) ipv4 or ipv6.

## Import

An instance is imported by its id or by a name only it has:

```shell
# By id, or by a name only it has. Its image, project and subscription are read
# back; what the API does not show (its SSH keys, user data, app inputs,
# password) is taken from the configuration on the next apply, without
# reinstalling or resetting anything.
terraform import masterdaweb_instance.web 4123
terraform import masterdaweb_instance.web web-1
```
