# Jambonz Terraform Provider

_Unofficial_ Terraform/OpenTofu provider for [jambonz](https://jambonz.org/),
generated from the jambonz API's OpenAPI description. A fork of
[hkrutzer/terraform-provider-jambonz](https://github.com/hkrutzer/terraform-provider-jambonz).

- [Resource and data source documentation](./docs)

## Installing

This fork is **not published to a provider registry**. The binary is built and
pushed to `ghcr.io/n-at-han-k/terraform-provider-jambonz` by
[`.github/workflows/build-image.yml`](.github/workflows/build-image.yml), in an
image whose only job is to carry it; consumers copy it out into a filesystem
mirror that OpenTofu resolves the provider from. The mirror layout is

```
<mirror>/<host>/<namespace>/<type>/<version>/<os>_<arch>/terraform-provider-<type>_v<version>
```

so the `source` address below is just the mirror path — nothing is fetched over
the network for it, and `ghcr.io` serves no registry.

```hcl
terraform {
  required_providers {
    jambonz = {
      source  = "ghcr.io/n-at-han-k/jambonz"
      version = "~> 1.0"
    }
  }
}
```

To build it locally instead, `go build .` and drop the binary into
`~/.terraform.d/plugins/` under the same layout, or point a `dev_overrides`
block at it.

## Configuring the provider

The provider takes two independent sets of credentials, because one resource
cannot use the REST API:

- `endpoint` + `api_key` — used by every resource except `jambonz_api_key`.
- `database` — used **only** by `jambonz_api_key`, which writes its row straight
  into the `api_keys` table. Every jambonz REST call authenticates with an API
  key, so the first key cannot be minted over the API that would need it.

```hcl
provider "jambonz" {
  endpoint = "https://jambonz.example.com/v1"
  api_key  = var.jambonz_api_key

  # Only jambonz_api_key uses this. Either a go-sql-driver DSN or a mysql:// URL.
  database = "jambones:${var.jambonz_db_password}@tcp(mysql.example.internal:3306)/jambones"
}
```

All three may instead come from `JAMBONZ_ENDPOINT`, `JAMBONZ_API_KEY` and
`JAMBONZ_DATABASE_URL`.

## Example: a SIP trunk that answers with an application

This provisions one end-to-end inbound path — a carrier trunk whose calls run a
voice application — with no DID to buy and no routing rules to write.

```hcl
locals {
  account_sid          = "00000000-0000-0000-0000-00000000acct"
  service_provider_sid = "00000000-0000-0000-0000-000000000sp1"
}

# Terraform's own key, so revoking it never takes out a credential someone uses
# by hand. Scope is decided by which sid is set: service_provider_sid scopes the
# key to a service provider, account_sid to one account, and neither makes it an
# admin key. Setting both is an error, and changing either replaces the key —
# jambonz has no way to rescope one.
#
# A service-provider key is what you want if the same config also manages the
# account: the API refuses to set an account's sip_realm when the caller
# authenticates with that account's own key.
resource "jambonz_api_key" "terraform" {
  service_provider_sid = local.service_provider_sid
}

output "jambonz_api_token" {
  value     = jambonz_api_key.terraform.token
  sensitive = true
}

# Adopting an account that already exists (jambonz seeds one on install) rather
# than creating a second. `name` here must match the existing row — a different
# name renames the account, it does not select a different one.
import {
  to = jambonz_account.default
  id = local.account_sid
}

resource "jambonz_account" "default" {
  name                 = "default account"
  service_provider_sid = local.service_provider_sid

  # The realm is what makes an account-level carrier routable at all. jambonz
  # matches an inbound INVITE to an account-scoped carrier only by way of its
  # account's sip_realm, so with this unset the SBC never finds the carrier
  # below and answers 404 however correct the rows look. It must also be the
  # host part of the request URI the caller dials — not merely the address the
  # packet arrives on.
  sip_realm = "sip.example.com"
}

# The application an inbound call runs. jambonz POSTs to call_hook and expects
# a JSON verb list back.
resource "jambonz_application" "voicebot" {
  name        = "voicebot"
  account_sid = local.account_sid

  # SET `url` AND NOTHING ELSE unless you need the rest. Every other hook field
  # is Optional *and* Computed: `method` is validated lowercase but stored
  # uppercase, and username/password read back null when sent as "", so pinning
  # them produces "inconsistent values for sensitive attribute" on apply.
  # Leaving them out lets the server's answer be the truth. jambonz defaults
  # the method to POST.
  call_hook = {
    url = "https://voicebot.example.com/call"
  }

  call_status_hook = {
    url = "https://voicebot.example.com/call-status"
  }
}

# The trunk. `application_sid` is what lets this work without a phone number:
# an inbound call on this carrier runs that application directly.
resource "jambonz_voip_carrier" "trunk" {
  name        = "upstream-trunk"
  description = "Inbound calls from the upstream SIP provider"

  # BOTH SIDS, and each does a different job. service_provider_sid is what lets
  # the SBC find this carrier by source address at all — that lookup ignores
  # carriers without one. account_sid and application_sid then do the routing
  # once it is found. It also lets a service-provider-scoped key read the row
  # back: the API compares the carrier's service_provider_sid with the key's.
  account_sid          = local.account_sid
  service_provider_sid = local.service_provider_sid
  application_sid      = jambonz_application.voicebot.application_sid

  # 1, NOT true. These are tinyint(1) columns that the API answers as numbers,
  # so the provider declares them integer.
  is_active = 1
}

# Where calls on that trunk are allowed to come from. A netmask narrower than 32
# is how you accept a peer whose source address moves within a known range.
resource "jambonz_sip_gateway" "trunk" {
  voip_carrier_sid = jambonz_voip_carrier.trunk.voip_carrier_sid
  ipv4             = "203.0.113.0"
  netmask          = 24
  port             = 5060
  inbound          = 1
  outbound         = 1
  is_active        = 1
}

# Optional: a DID pointed at the same application, if you do have a number.
resource "jambonz_phone_number" "main" {
  number           = "+15551234567"
  voip_carrier_sid = jambonz_voip_carrier.trunk.voip_carrier_sid
  account_sid      = local.account_sid
  application_sid  = jambonz_application.voicebot.application_sid
}
```

### Bootstrapping in one pass

`jambonz_api_key` needs only `database`, while every other resource needs
`endpoint` and `api_key` at **plan** time — so a config that mints its own key
cannot also use it on the same run. The provider errors during `Configure` if
`endpoint` is set but `api_key` is empty, which would fail the whole workspace
before the key resource ever runs. Gate both on the key's presence and let the
API-backed resources appear on the next apply:

```hcl
provider "jambonz" {
  # null, not "" — an empty string still counts as "the API was configured".
  endpoint = fileexists("api-key.txt") ? "https://jambonz.example.com/v1" : null
  api_key  = fileexists("api-key.txt") ? trimspace(file("api-key.txt")) : null
  database = var.jambonz_database_url
}

resource "jambonz_application" "voicebot" {
  count = fileexists("api-key.txt") ? 1 : 0
  # ...
}
```

`fileexists`, not a bare `file()`: a missing `file()` is a plan-time error and
would take every other resource in the module down with it.
