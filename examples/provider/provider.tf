provider "jambonz" {
  # Both may also come from the environment, as JAMBONZ_ENDPOINT and
  # JAMBONZ_API_KEY.
  endpoint = "https://jambonz.cloud/api/v1"
  api_key  = var.jambonz_api_key

  # Only jambonz_api_key uses this, and only because the REST API cannot create
  # the key that would authenticate the call. May also come from the
  # environment, as JAMBONZ_DATABASE_URL. A configuration that manages nothing
  # but API keys can set this and leave the two above out.
  database = "mysql://jambones:${var.db_password}@mysql:3306/jambones"
}
