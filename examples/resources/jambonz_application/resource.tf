data "jambonz_account" "acme" {
  account_sid = "b42f0f47-3972-4361-a2a4-e69cf0e1e8c3"
}

resource "jambonz_application" "support_line" {
  name        = "Support line"
  account_sid = data.jambonz_account.acme.account_sid

  # 0 or 1 — the API models this as an integer, not a boolean.
  record_all_calls = 0

  call_hook = {
    url      = "https://example.com/calls"
    method   = "POST"
    username = "user"
    password = var.webhook_password
  }

  call_status_hook = {
    url    = "https://example.com/status"
    method = "POST"
  }

  # Required by the API. The URL may be empty when no messaging hook is wanted.
  messaging_hook = {
    url    = ""
    method = "POST"
  }
}
