resource "jambonz_sip_gateway" "fastco_east" {
  voip_carrier_sid = jambonz_voip_carrier.fastco.voip_carrier_sid

  ipv4    = "198.51.100.10"
  port    = 5060
  netmask = 32

  inbound  = true
  outbound = true
}
