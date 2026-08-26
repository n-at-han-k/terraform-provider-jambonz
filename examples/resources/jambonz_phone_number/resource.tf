resource "jambonz_phone_number" "main" {
  number      = "15551234567"
  account_sid = jambonz_account.acme.account_sid

  # The carrier a number is provisioned from cannot be changed in place, so
  # changing it replaces the number.
  voip_carrier_sid = jambonz_voip_carrier.fastco.voip_carrier_sid

  application_sid = jambonz_application.support_line.application_sid
}
