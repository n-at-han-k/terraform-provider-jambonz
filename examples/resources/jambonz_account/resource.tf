resource "jambonz_account" "acme" {
  name                 = "Acme Telecom"
  service_provider_sid = "85f9c036-ba61-4f28-b2f5-617c23fa68ff"
  sip_realm            = "sip.acme.example"

  registration_hook = {
    url    = "https://acme.example/register"
    method = "POST"

    # Optional basic auth. The API returns the password only when it is set, so
    # leaving it out here leaves it out of state too.
    username = "acme"
    password = var.registration_password
  }
}
