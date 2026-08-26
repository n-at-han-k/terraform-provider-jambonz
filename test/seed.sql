-- A known admin api key.
--
-- `npm run upgrade-db` seeds an admin key too, but with a freshly generated
-- token, so it cannot be written down in a test. This adds a second key with a
-- fixed token, which is what JAMBONZ_API_KEY is set to below and in
-- provider_test.go. Admin scope is account_sid IS NULL and
-- service_provider_sid IS NULL — the same shape the migration's own key has.
-- Idempotent, because `docker compose up` is: bringing the stack back up over
-- an existing volume re-runs this, and a duplicate-key error there fails the
-- whole `up` rather than doing nothing.
delete from api_keys where api_key_sid = '3f35518f-5a0d-4c2e-90a5-2407bb3b36f0';
insert into api_keys (api_key_sid, token)
values ('3f35518f-5a0d-4c2e-90a5-2407bb3b36f0', '38700987-c7a4-4685-a5bb-af378f9734de');
