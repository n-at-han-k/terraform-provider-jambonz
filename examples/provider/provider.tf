provider "jambonz" {
  # Both may also come from the environment, as JAMBONZ_ENDPOINT and
  # JAMBONZ_API_KEY.
  endpoint = "https://jambonz.cloud/api/v1"
  api_key  = var.jambonz_api_key
}
