resource "jambonz_voip_carrier" "fastco" {
  name        = "fastco"
  description = "US SIP trunking provider"
  account_sid = jambonz_account.acme.account_sid

  e164_leading_plus = true
  tech_prefix       = "1234"

  # Registration, when the carrier requires us to REGISTER to receive calls.
  requires_register  = true
  register_use_tls   = true
  register_username  = "acme"
  register_sip_realm = "sip.fastco.example"
  register_password  = var.carrier_password
}
