# The bootstrap key: no account_sid and no service_provider_sid, so it is an
# admin key, the scope jambonz's own db/create-admin-token.sql creates.
#
# This resource is written straight to the database, because every REST call
# authenticates with an API key and this is the key that would authenticate it.
# So the provider needs `database` here, not `endpoint` and `api_key`.
resource "jambonz_api_key" "admin" {}

# An account-scoped key, for whatever runs calls against that account. Setting
# service_provider_sid instead scopes the key to a service provider; setting
# both is an error.
resource "jambonz_api_key" "acme" {
  account_sid = jambonz_account.acme.account_sid
}

output "admin_token" {
  value     = jambonz_api_key.admin.token
  sensitive = true
}
